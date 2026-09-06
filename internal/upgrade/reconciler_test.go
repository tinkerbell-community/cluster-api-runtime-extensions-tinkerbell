package upgrade

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/state"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeTalos struct {
	version semver.Version
	err     error
}

func (f *fakeTalos) ObservedVersion(context.Context, string) (semver.Version, error) {
	return f.version, f.err
}
func (f *fakeTalos) State() state.State { return nil }
func (f *fakeTalos) Close() error       { return nil }

// harness wires a Reconciler with fully faked externals.
type harness struct {
	r         *Reconciler
	syncCalls *[]SyncOptions
	syncErr   error
}

func cpMachine(name string, upToDate bool, addr string) *clusterv1.Machine {
	status := metav1.ConditionTrue
	if !upToDate {
		status = metav1.ConditionFalse
	}
	m := machine(name, "wl", "v1.36.4", &status)
	m.Labels[clusterv1.MachineControlPlaneLabel] = ""
	if addr != "" {
		m.Status.Addresses = clusterv1.MachineAddresses{{Type: clusterv1.MachineInternalIP, Address: addr}}
	}
	return m
}

func secret(name, key string, value []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tink"},
		Data:       map[string][]byte{key: value},
	}
}

func convergedWorkload() *k8sfake.Clientset {
	return k8sfake.NewClientset(
		node("n1", "v1.36.4"),
		staticPod("apiserver-a", "kube-apiserver", "registry.k8s.io/kube-apiserver:v1.36.4"),
	)
}

func newHarness(t *testing.T, workload *k8sfake.Clientset, syncErr error, mgmtObjs ...runtime.Object) *harness {
	t.Helper()
	calls := []SyncOptions{}
	h := &harness{syncCalls: &calls, syncErr: syncErr}
	c := fake.NewClientBuilder().WithScheme(upgradeScheme(t)).WithRuntimeObjects(mgmtObjs...).Build()
	h.r = &Reconciler{
		Client:   c,
		Recorder: events.NewFakeRecorder(50),
		NewWorkload: func([]byte) (kubernetes.Interface, *rest.Config, error) {
			return workload, &rest.Config{Host: "fake"}, nil
		},
		NewTalos: func(context.Context, []byte, []string) (TalosConn, error) {
			return &fakeTalos{version: semver.MustParse("1.13.9")}, nil
		},
		Sync: func(_ context.Context, _ state.State, _ *rest.Config, _ semver.Version, o SyncOptions) (SyncReport, error) {
			calls = append(calls, o)
			if h.syncErr != nil {
				return SyncReport{}, h.syncErr
			}
			return SyncReport{Created: 1, Configured: 2}, nil
		},
		Opts: Options{GateRequeueInterval: time.Minute, ReconcileTimeout: 5 * time.Minute},
	}
	return h
}

func reconcileOnce(t *testing.T, h *harness) (ctrl.Result, error) {
	t.Helper()
	return h.r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKey{Namespace: "tink", Name: "cp"},
	})
}

func fullMgmtSet(annotations map[string]any) []runtime.Object {
	owner := []any{map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Cluster", "name": "wl", "uid": "u1",
	}}
	return []runtime.Object{
		tcpFixture(map[string]any{"version": "v1.36.4"}, annotations, owner, nil),
		cpMachine("cp-1", true, "10.0.0.1"),
		secret("wl-kubeconfig", "value", []byte("kubeconfig")),
		secret("wl-talosconfig", "talosconfig", []byte("talosconfig")),
	}
}

func currentTCP(t *testing.T, h *harness) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(TalosControlPlaneGVK)
	if err := h.r.Get(context.Background(), client.ObjectKey{Namespace: "tink", Name: "cp"}, u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestReconcileSyncsAndCheckpoints(t *testing.T) {
	h := newHarness(t, convergedWorkload(), nil, fullMgmtSet(nil)...)
	res, err := reconcileOnce(t, h)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0", res.RequeueAfter)
	}
	if len(*h.syncCalls) != 1 {
		t.Fatalf("sync called %d times, want 1", len(*h.syncCalls))
	}
	if got := LastSynced(currentTCP(t, h)); got != "k8s=v1.36.4,talos=v1.13.9" {
		t.Errorf("checkpoint = %q", got)
	}
}

func TestReconcileGateBlockedRequeues(t *testing.T) {
	objs := fullMgmtSet(nil)
	objs = append(objs, machine("worker-1", "wl", "v1.36.4", condFalse()))
	h := newHarness(t, convergedWorkload(), nil, objs...)
	res, err := reconcileOnce(t, h)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if res.RequeueAfter != time.Minute {
		t.Errorf("RequeueAfter = %v, want gate interval", res.RequeueAfter)
	}
	if len(*h.syncCalls) != 0 {
		t.Error("sync ran despite blocked gate")
	}
	if LastSynced(currentTCP(t, h)) != "" {
		t.Error("checkpoint written despite blocked gate")
	}
}

func TestReconcileAlreadySyncedIsNoop(t *testing.T) {
	h := newHarness(t, convergedWorkload(), nil,
		fullMgmtSet(map[string]any{LastSyncedAnnotation: "k8s=v1.36.4,talos=v1.13.9"})...)
	res, err := reconcileOnce(t, h)
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("Reconcile() = %v, %v", res, err)
	}
	if len(*h.syncCalls) != 0 {
		t.Error("sync ran for an already-synced pair")
	}
}

func TestReconcileForceSyncBypassesGateAndClears(t *testing.T) {
	objs := fullMgmtSet(map[string]any{
		LastSyncedAnnotation: "k8s=v1.36.4,talos=v1.13.9", // pair equal — force still syncs
		ForceSyncAnnotation:  "true",
	})
	objs = append(objs, machine("worker-1", "wl", "v1.36.4", condFalse())) // gate would block
	h := newHarness(t, convergedWorkload(), nil, objs...)
	if _, err := reconcileOnce(t, h); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if len(*h.syncCalls) != 1 {
		t.Fatalf("sync called %d times under force-sync, want 1", len(*h.syncCalls))
	}
	if ForceSyncRequested(currentTCP(t, h)) {
		t.Error("force-sync annotation not cleared after honoring")
	}
}

func TestReconcileMissingSecretRequeuesQuietly(t *testing.T) {
	owner := []any{map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Cluster", "name": "wl", "uid": "u1",
	}}
	h := newHarness(t, convergedWorkload(), nil,
		tcpFixture(map[string]any{"version": "v1.36.4"}, nil, owner, nil),
		cpMachine("cp-1", true, "10.0.0.1"),
	)
	res, err := reconcileOnce(t, h)
	if err != nil {
		t.Fatalf("Reconcile() = %v, want quiet requeue", err)
	}
	if res.RequeueAfter != 5*time.Minute {
		t.Errorf("RequeueAfter = %v, want 5m", res.RequeueAfter)
	}
	if len(*h.syncCalls) != 0 {
		t.Error("sync ran without secrets")
	}
}

func TestReconcileNoUpToDateCPMachine(t *testing.T) {
	owner := []any{map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Cluster", "name": "wl", "uid": "u1",
	}}
	h := newHarness(t, convergedWorkload(), nil,
		tcpFixture(map[string]any{"version": "v1.36.4"}, nil, owner, nil),
		cpMachine("cp-1", false, "10.0.0.1"),
		secret("wl-kubeconfig", "value", []byte("k")),
		secret("wl-talosconfig", "talosconfig", []byte("t")),
	)
	res, err := reconcileOnce(t, h)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if res.RequeueAfter != time.Minute {
		t.Errorf("RequeueAfter = %v, want gate interval", res.RequeueAfter)
	}
	if len(*h.syncCalls) != 0 {
		t.Error("sync ran with no UpToDate control-plane machine")
	}
}

func TestReconcileSyncFailureRetries(t *testing.T) {
	h := newHarness(t, convergedWorkload(), errors.New("apply exploded"), fullMgmtSet(nil)...)
	if _, err := reconcileOnce(t, h); err == nil {
		t.Fatal("Reconcile() = nil error, want sync failure")
	}
	if LastSynced(currentTCP(t, h)) != "" {
		t.Error("checkpoint written despite failed sync")
	}
}

func TestReconcileDryRunSkipsCheckpoint(t *testing.T) {
	h := newHarness(t, convergedWorkload(), nil, fullMgmtSet(nil)...)
	h.r.Opts.DryRun = true
	if _, err := reconcileOnce(t, h); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if len(*h.syncCalls) != 1 || !(*h.syncCalls)[0].DryRun {
		t.Fatalf("sync calls = %+v, want one dry-run call", *h.syncCalls)
	}
	if LastSynced(currentTCP(t, h)) != "" {
		t.Error("checkpoint written in dry-run mode")
	}
}
