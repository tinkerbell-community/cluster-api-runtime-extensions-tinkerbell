package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

//nolint:unparam // fixture: name is a real dimension of TinkerbellMachine, held at "m1" by this task's cases.
func tinkerbellMachine(name string, annotations map[string]string) *unstructured.Unstructured {
	tm := &unstructured.Unstructured{}
	tm.SetGroupVersionKind(TinkerbellMachineGVK)
	tm.SetNamespace("tinkerbell")
	tm.SetName(name)
	tm.SetAnnotations(annotations)
	_ = unstructured.SetNestedField(tm.Object, "hw-1", "spec", "hardwareName")
	return tm
}

//nolint:unparam // fixture: arch is a real dimension of Hardware, held at "x86_64" by this task's cases.
func claimedBy(tm *unstructured.Unstructured, annotations map[string]string, arch string) *tinkv1.Hardware {
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-1", Namespace: "tinkerbell",
		Labels:      map[string]string{OwnerNameLabel: tm.GetName(), OwnerNamespaceLabel: tm.GetNamespace()},
		Annotations: annotations,
	}}
	hw.Spec.Interfaces = []tinkv1.Interface{{DHCP: &tinkv1.DHCP{Arch: arch}}}
	return hw
}

func testReconciler(t *testing.T) *Reconciler {
	t.Helper()
	rsrv := versionsServer(t, nil, realisticVersions())
	t.Cleanup(rsrv.Close)
	factory := "http://factory.example.test"
	return &Reconciler{
		Registrar:     stubRegistrar(t, "sid123"),
		Policy:        VersionPolicy{Versions: NewVersionResolver(rsrv.URL)},
		Customization: CustomizationConfig{TootlesUserDataURL: testTootles},
		FactoryURL:    factory,
	}
}

func stubRegistrar(t *testing.T, id string) *Registrar {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}))
	t.Cleanup(srv.Close)
	return NewRegistrar(srv.URL)
}

func TestIsClaimedPredicate(t *testing.T) {
	t.Parallel()
	tm := tinkerbellMachine("m1", nil)
	if !isClaimed(claimedBy(tm, nil, "x86_64"), tm) {
		t.Error("owner labels matching the machine must be claimed")
	}
	unclaimed := claimedBy(tm, nil, "x86_64")
	unclaimed.Labels = nil
	if isClaimed(unclaimed, tm) {
		t.Error("hardware with no owner labels must not be claimed")
	}
	wrong := claimedBy(tm, nil, "x86_64")
	wrong.Labels[OwnerNameLabel] = "someone-else"
	if isClaimed(wrong, tm) {
		t.Error("hardware owned by another machine must not be claimed")
	}
}

func TestResolveUnprovisionedWritesFullBlock(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, nil, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.OperatingSystem == nil {
		t.Fatal("unprovisioned claimed hardware must get an operating_system block")
	}
	os := plan.OperatingSystem
	if os.Slug != "sid123" || os.Version != "v1.13.9" || os.ImageTag != "v1.13.9" || os.Distro != "talos" || os.OsSlug != "talos-v1.13.9-amd64" {
		t.Errorf("operating_system = %+v", os)
	}
	if plan.InstallerImage != "factory.example.test/metal-installer/sid123:v1.13.9" {
		t.Errorf("installer = %q", plan.InstallerImage)
	}
}

// §3.5 provisioned-freeze: do NOT recompute operating_system, but DO refresh installer-image.
func TestResolveProvisionedFreezesOSButRefreshesInstaller(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, map[string]string{ProvisionedAnnotation: "true"}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.OperatingSystem != nil {
		t.Fatal("provisioned hardware must not get a recomputed operating_system block")
	}
	if plan.InstallerImage == "" {
		t.Error("provisioned hardware must still refresh the installer-image annotation")
	}
}

// §3.4 empty result ⇒ write nothing.
func TestResolveEmptyVersionWritesNothing(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, map[string]string{ProvisionedAnnotation: "true"}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "") // provisioned + unpinned ⇒ ""
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatalf("expected no plan, got %+v", plan)
	}
}

// talos2disk records the schematic it actually installed; the resolver must not replace it
// with its own, smaller schematic, or the upgrade path would drop detected extensions.
func TestResolveKeepsTalos2diskInstallerImage(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	installed := "factory.example.test/metal-installer/installedbyaction:v1.13.9"
	hw := claimedBy(tm, map[string]string{
		UserDataOwnerAnnotation:  UserDataOwnerTalos2disk,
		InstallerImageAnnotation: installed,
	}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		t.Fatal("expected a plan")
	}
	if plan.InstallerImage != installed {
		t.Errorf("installer = %q, want the action-recorded %q", plan.InstallerImage, installed)
	}
	if plan.OperatingSystem == nil || plan.OperatingSystem.Slug != "sid123" {
		t.Errorf("operating_system must still be resolved for an unprovisioned machine, got %+v", plan.OperatingSystem)
	}
}

func TestResolveIgnoresOwnerWithoutInstallerImage(t *testing.T) {
	t.Parallel()
	r := testReconciler(t)
	tm := tinkerbellMachine("m1", nil)
	hw := claimedBy(tm, map[string]string{UserDataOwnerAnnotation: UserDataOwnerTalos2disk}, "x86_64")

	plan, err := r.resolve(context.Background(), tm, hw, "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstallerImage != "factory.example.test/metal-installer/sid123:v1.13.9" {
		t.Errorf("without a recorded image the resolver's own value applies, got %q", plan.InstallerImage)
	}
}
