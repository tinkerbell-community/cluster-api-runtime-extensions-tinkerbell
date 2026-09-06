package upgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/blang/semver/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func upgradeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	return scheme
}

func machine(name, cluster, version string, upToDate *metav1.ConditionStatus) *clusterv1.Machine {
	m := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "tink",
		Labels: map[string]string{clusterv1.ClusterNameLabel: cluster},
	}}
	m.Spec.Version = version
	m.Spec.ClusterName = cluster
	if upToDate != nil {
		m.Status.Conditions = []metav1.Condition{{
			Type: clusterv1.MachineUpToDateCondition, Status: *upToDate, Reason: "test",
		}}
	}
	return m
}

func node(name, kubelet string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	n.Status.NodeInfo.KubeletVersion = kubelet
	return n
}

func staticPod(name, app, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kube-system",
			Labels: map[string]string{"k8s-app": app},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: app, Image: image}}},
	}
}

func condTrue() *metav1.ConditionStatus  { s := metav1.ConditionTrue; return &s }
func condFalse() *metav1.ConditionStatus { s := metav1.ConditionFalse; return &s }

func TestEvaluateGate(t *testing.T) {
	target := semver.MustParse("1.36.4")
	healthyPods := []runtime.Object{
		staticPod("apiserver-a", "kube-apiserver", "registry.k8s.io/kube-apiserver:v1.36.4"),
		staticPod("cm-a", "kube-controller-manager", "registry.k8s.io/kube-controller-manager:v1.36.4"),
		staticPod("sched-a", "kube-scheduler", "registry.k8s.io/kube-scheduler:v1.36.4"),
	}

	tests := []struct {
		name        string
		machines    []*clusterv1.Machine
		workload    []runtime.Object
		ignoreNodes sets.Set[string]
		converged   bool
		blockerHint string
	}{
		{
			name:      "all converged",
			machines:  []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue())},
			workload:  append([]runtime.Object{node("n1", "v1.36.4")}, healthyPods...),
			converged: true,
		},
		{
			name:        "machine not UpToDate blocks",
			machines:    []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condFalse())},
			workload:    append([]runtime.Object{node("n1", "v1.36.4")}, healthyPods...),
			converged:   false,
			blockerHint: "m1",
		},
		{
			name:        "machine without condition falls back to minor equality",
			machines:    []*clusterv1.Machine{machine("m1", "wl", "v1.36.1", nil)},
			workload:    append([]runtime.Object{node("n1", "v1.36.4")}, healthyPods...),
			converged:   true,
			blockerHint: "",
		},
		{
			name:        "machine without condition and wrong minor blocks",
			machines:    []*clusterv1.Machine{machine("m1", "wl", "v1.35.2", nil)},
			workload:    append([]runtime.Object{node("n1", "v1.36.4")}, healthyPods...),
			converged:   false,
			blockerHint: "m1",
		},
		{
			name:     "lagging bootstrap node kubelet blocks",
			machines: []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue())},
			workload: append([]runtime.Object{
				node("n1", "v1.36.4"), node("bootstrap", "v1.35.3"),
			}, healthyPods...),
			converged:   false,
			blockerHint: "bootstrap",
		},
		{
			name:     "ignored node is skipped",
			machines: []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue())},
			workload: append([]runtime.Object{
				node("n1", "v1.36.4"), node("dead", "v1.35.3"),
			}, healthyPods...),
			ignoreNodes: sets.New("dead"),
			converged:   true,
		},
		{
			name:     "lagging static pod blocks (bootstrap node apiserver)",
			machines: []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue())},
			workload: []runtime.Object{
				node("n1", "v1.36.4"),
				staticPod("apiserver-a", "kube-apiserver", "registry.k8s.io/kube-apiserver:v1.36.4"),
				staticPod("apiserver-b", "kube-apiserver", "registry.k8s.io/kube-apiserver:v1.35.1"),
			},
			converged:   false,
			blockerHint: "1.35",
		},
		{
			name:        "no apiserver pod at all blocks",
			machines:    []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue())},
			workload:    []runtime.Object{node("n1", "v1.36.4")},
			converged:   false,
			blockerHint: "kube-apiserver",
		},
		{
			name:      "foreign-cluster machine ignored",
			machines:  []*clusterv1.Machine{machine("m1", "wl", "v1.36.4", condTrue()), machine("mx", "other", "v1.20.0", condFalse())},
			workload:  append([]runtime.Object{node("n1", "v1.36.4")}, healthyPods...),
			converged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tt.machines))
			for _, m := range tt.machines {
				objs = append(objs, m)
			}
			mgmt := fake.NewClientBuilder().WithScheme(upgradeScheme(t)).WithRuntimeObjects(objs...).Build()
			workload := k8sfake.NewClientset(tt.workload...)

			got, err := EvaluateGate(context.Background(), mgmt, workload, "wl", "tink", target, tt.ignoreNodes)
			if err != nil {
				t.Fatalf("EvaluateGate() error = %v", err)
			}
			if got.Converged != tt.converged {
				t.Fatalf("Converged = %v, want %v (blockers: %v)", got.Converged, tt.converged, got.Blockers)
			}
			if tt.blockerHint != "" && !tt.converged {
				joined := strings.Join(got.Blockers, "; ")
				if !strings.Contains(joined, tt.blockerHint) {
					t.Errorf("blockers %q do not mention %q", joined, tt.blockerHint)
				}
			}
		})
	}
}

// TestEvaluateGateFallbackNoted asserts the condition-less fallback is surfaced so the
// reconciler can emit its event.
func TestEvaluateGateFallbackNoted(t *testing.T) {
	mgmt := fake.NewClientBuilder().WithScheme(upgradeScheme(t)).
		WithRuntimeObjects(machine("m1", "wl", "v1.36.1", nil)).Build()
	workload := k8sfake.NewClientset(
		node("n1", "v1.36.4"),
		staticPod("apiserver-a", "kube-apiserver", "registry.k8s.io/kube-apiserver:v1.36.4"),
	)
	got, err := EvaluateGate(context.Background(), mgmt, workload, "wl", "tink", semver.MustParse("1.36.4"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FallbackMachines) != 1 || got.FallbackMachines[0] != "m1" {
		t.Errorf("FallbackMachines = %v, want [m1]", got.FallbackMachines)
	}
}
