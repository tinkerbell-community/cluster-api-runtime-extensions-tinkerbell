//go:build envtest

package janitor

// The concurrency design — server-side label-selector transitions and
// resourceVersion conflicts — is exactly what fake clients fake badly, so
// these scenarios run against a real API server. Run with: make test-envtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	envCfg    *rest.Config
	envClient client.WithWatch
)

func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "test", "crds")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	envCfg = cfg
	envClient, err = client.NewWithWatch(cfg, client.Options{Scheme: envScheme()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building client: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

func envScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tinkv1.AddToScheme(scheme))
	return scheme
}

// newNamespace creates an isolated namespace per test.
func newNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "janitor-envtest-"}}
	if err := envClient.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	return ns.Name
}

// claimedHardware builds a CAPT-shaped claimed object.
func claimedHardware(namespace string) *tinkv1.Hardware {
	hw := releasedHardware()
	hw.Namespace = namespace
	hw.Labels = map[string]string{
		OwnerNameLabel:      "machine-1",
		OwnerNamespaceLabel: namespace,
	}
	hw.Annotations = map[string]string{ProvisionedAnnotation: "true"}
	return hw
}

// releaseHW mirrors CAPT's releaseHardware: remove owner labels and the
// provisioned annotation.
func releaseHW(t *testing.T, hw *tinkv1.Hardware) {
	t.Helper()
	fresh := &tinkv1.Hardware{}
	if err := envClient.Get(context.Background(), client.ObjectKeyFromObject(hw), fresh); err != nil {
		t.Fatal(err)
	}
	delete(fresh.Labels, OwnerNameLabel)
	delete(fresh.Labels, OwnerNamespaceLabel)
	delete(fresh.Annotations, ProvisionedAnnotation)
	if err := envClient.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
}

// TestEnvtestJanitorReleaseTransition runs the real manager wiring (filtered
// cache on owner-label absence) and asserts a CAPT-shaped release flows
// through the selector watch into a scrub.
func TestEnvtestJanitorReleaseTransition(t *testing.T) {
	namespace := newNamespace(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selector, err := labels.Parse("!" + OwnerNameLabel)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(envCfg, ctrl.Options{
		Scheme:  envScheme(),
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{namespace: {}},
			ByObject: map[client.Object]cache.ByObject{
				&tinkv1.Hardware{}: {Label: selector},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: mgr.GetClient(), Recorder: events.NewFakeRecorder(16), Opts: testOptions()}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}

	hw := claimedHardware(namespace)
	if err := envClient.Create(ctx, hw); err != nil {
		t.Fatal(err)
	}
	// Claimed: outside the selector, never cached, never scrubbed.
	time.Sleep(500 * time.Millisecond)
	fresh := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(hw), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.UserData == nil {
		t.Fatal("claimed hardware was scrubbed")
	}

	releaseHW(t, hw)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := envClient.Get(ctx, client.ObjectKeyFromObject(hw), fresh); err != nil {
			t.Fatal(err)
		}
		if fresh.Spec.UserData == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released hardware was not scrubbed within the deadline")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if fresh.Spec.Metadata.Instance.OperatingSystem != nil {
		t.Error("operating_system not cleared")
	}
	if nb := fresh.Spec.Interfaces[0].Netboot; nb.AllowPXE == nil || *nb.AllowPXE {
		t.Error("allowPXE not parked")
	}
	if fresh.Annotations[ScrubbedAtAnnotation] == "" {
		t.Error("scrubbed-at not stamped")
	}
}

// TestEnvtestJanitorReclaimRace interleaves a re-claim between the janitor's
// read and its update: the write must fail with a real 409, and the new
// claim's userData must survive.
func TestEnvtestJanitorReclaimRace(t *testing.T) {
	namespace := newNamespace(t)
	ctx := context.Background()

	hw := claimedHardware(namespace)
	if err := envClient.Create(ctx, hw); err != nil {
		t.Fatal(err)
	}
	releaseHW(t, hw)

	var raced bool
	racing := interceptor.NewClient(envClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if _, ok := obj.(*tinkv1.Hardware); !ok || raced {
				return nil
			}
			raced = true
			// Re-claim lands between the janitor's read and its update.
			reclaim := &tinkv1.Hardware{}
			if err := c.Get(ctx, key, reclaim); err != nil {
				return err
			}
			reclaim.Labels = map[string]string{OwnerNameLabel: "machine-2", OwnerNamespaceLabel: namespace}
			reclaim.Spec.UserData = ptr.To("#cloud-config NEW machine config")
			return c.Update(ctx, reclaim)
		},
	})
	r := &Reconciler{Client: racing, Recorder: events.NewFakeRecorder(16), Opts: testOptions()}

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hw)})
	if err == nil {
		t.Fatal("expected the guarded update to fail")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("expected Conflict, got %v", err)
	}

	// The fresh reconcile re-classifies as Claimed and must not touch the
	// new machine's config.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hw)}); err != nil {
		t.Fatal(err)
	}
	fresh := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(hw), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.UserData == nil || *fresh.Spec.UserData != "#cloud-config NEW machine config" {
		t.Errorf("the new claim's userData was damaged: %v", fresh.Spec.UserData)
	}
}
