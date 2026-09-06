package upgrade

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func tcpFixture(spec map[string]any, annotations map[string]any, owners []any, labels map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "controlplane.cluster.x-k8s.io/v1beta1",
		"kind":       "TalosControlPlane",
		"metadata": map[string]any{
			"name":      "cp",
			"namespace": "tink",
		},
	}}
	if spec != nil {
		u.Object["spec"] = spec
	}
	if annotations != nil {
		_ = unstructured.SetNestedMap(u.Object, annotations, "metadata", "annotations")
	}
	if owners != nil {
		_ = unstructured.SetNestedSlice(u.Object, owners, "metadata", "ownerReferences")
	}
	if labels != nil {
		_ = unstructured.SetNestedMap(u.Object, labels, "metadata", "labels")
	}
	return u
}

func TestTCPVersion(t *testing.T) {
	if _, err := TCPVersion(tcpFixture(nil, nil, nil, nil)); err == nil {
		t.Error("TCPVersion(no spec) = nil error, want error")
	}
	got, err := TCPVersion(tcpFixture(map[string]any{"version": "v1.36.4"}, nil, nil, nil))
	if err != nil || got != "v1.36.4" {
		t.Errorf("TCPVersion() = %q, %v", got, err)
	}
}

func TestTCPTalosVersion(t *testing.T) {
	if got := TCPTalosVersion(tcpFixture(nil, nil, nil, nil)); got != "" {
		t.Errorf("TCPTalosVersion(absent) = %q, want empty", got)
	}
	spec := map[string]any{"controlPlaneConfig": map[string]any{"controlplane": map[string]any{"talosVersion": "v1.13"}}}
	if got := TCPTalosVersion(tcpFixture(spec, nil, nil, nil)); got != "v1.13" {
		t.Errorf("TCPTalosVersion() = %q", got)
	}
}

func TestLastSyncedAndForceSync(t *testing.T) {
	u := tcpFixture(nil, map[string]any{
		LastSyncedAnnotation: "k8s=v1.36.4,talos=v1.13.9",
		ForceSyncAnnotation:  "true",
	}, nil, nil)
	if got := LastSynced(u); got != "k8s=v1.36.4,talos=v1.13.9" {
		t.Errorf("LastSynced() = %q", got)
	}
	if !ForceSyncRequested(u) {
		t.Error("ForceSyncRequested() = false, want true")
	}
	if ForceSyncRequested(tcpFixture(nil, nil, nil, nil)) {
		t.Error("ForceSyncRequested(absent) = true, want false")
	}
}

func TestClusterNameFor(t *testing.T) {
	owner := []any{map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2", "kind": "Cluster", "name": "workload", "uid": "u1",
	}}
	got, err := ClusterNameFor(tcpFixture(nil, nil, owner, nil))
	if err != nil || got != "workload" {
		t.Errorf("ClusterNameFor(owner) = %q, %v", got, err)
	}

	labeled := tcpFixture(nil, nil, nil, map[string]any{"cluster.x-k8s.io/cluster-name": "labeled"})
	got, err = ClusterNameFor(labeled)
	if err != nil || got != "labeled" {
		t.Errorf("ClusterNameFor(label fallback) = %q, %v", got, err)
	}

	if _, err := ClusterNameFor(tcpFixture(nil, nil, nil, nil)); err == nil {
		t.Error("ClusterNameFor(no owner, no label) = nil error, want error")
	}
}

func TestPairString(t *testing.T) {
	p := Pair{K8s: "v1.36.4", Talos: "v1.13.9"}
	if got := p.String(); got != "k8s=v1.36.4,talos=v1.13.9" {
		t.Errorf("Pair.String() = %q", got)
	}
}
