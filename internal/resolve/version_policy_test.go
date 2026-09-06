package resolve

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func policy(t *testing.T, versions []string) VersionPolicy {
	t.Helper()
	srv := versionsServer(t, nil, versions)
	t.Cleanup(srv.Close)
	return VersionPolicy{Versions: NewVersionResolver(srv.URL)}
}

func TestResolveFullPinUsedExactly(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "v1.13.9", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.13.9" || pin != "" {
		t.Fatalf("Resolve(full pin) = (%q,%q), want (v1.13.9,\"\")", v, pin)
	}
}

func TestResolveBareMinorTracksLatestPatch(t *testing.T) {
	t.Parallel()
	v, _, err := policy(t, realisticVersions()).Resolve(context.Background(), "v1.13", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "v1.13.10" {
		t.Fatalf("Resolve(v1.13) = %q, want v1.13.10", v)
	}
}

func TestResolveUnsetUnprovisionedPinsNewestMinor(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if pin != "v1.14" || v != "v1.14.0" {
		t.Fatalf("Resolve(unset, unprovisioned) = (%q, pin %q), want (v1.14.0, v1.14)", v, pin)
	}
}

func TestResolveExistingPinSkipsNewLatestMinor(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", false, "v1.13")
	if err != nil {
		t.Fatal(err)
	}
	if pin != "" || v != "v1.13.10" {
		t.Fatalf("Resolve(unset, pinned v1.13) = (%q, newPin %q), want (v1.13.10, \"\")", v, pin)
	}
}

// A provisioned machine with no pin is left unresolved so the resolver can never ask CABPT
// to skip a minor (Talos forbids it).
func TestResolveProvisionedUnpinnedReturnsEmpty(t *testing.T) {
	t.Parallel()
	v, pin, err := policy(t, realisticVersions()).Resolve(context.Background(), "", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if v != "" || pin != "" {
		t.Fatalf("Resolve(unset, provisioned) = (%q,%q), want (\"\",\"\")", v, pin)
	}
}

func TestSpecTalosVersionReadsBootstrapConfigUnstructured(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)

	tc := &unstructured.Unstructured{}
	tc.SetGroupVersionKind(schema.GroupVersionKind{Group: "bootstrap.cluster.x-k8s.io", Version: "v1beta1", Kind: "TalosConfig"})
	tc.SetNamespace("tinkerbell")
	tc.SetName("tc-1")
	_ = unstructured.SetNestedField(tc.Object, "v1.13.9", "spec", "talosVersion")

	machine := &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Namespace: "tinkerbell", Name: "m1"}}
	machine.Spec.Bootstrap.ConfigRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: "bootstrap.cluster.x-k8s.io", Kind: "TalosConfig", Name: "tc-1",
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc).Build()
	if got := SpecTalosVersion(context.Background(), c, machine); got != "v1.13.9" {
		t.Fatalf("SpecTalosVersion = %q, want v1.13.9", got)
	}
}
