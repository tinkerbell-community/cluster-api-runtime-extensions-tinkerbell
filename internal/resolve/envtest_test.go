//go:build envtest

package resolve

// The SSA managed-fields behavior — sole ownership of operating_system, coexistence handoff
// from the old talos-os-metadata manager, and the resourceVersion precondition — is exactly
// what the fake client fakes badly, so it runs against a real API server. Run: make test-envtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	envCfg     *rest.Config
	envClient  client.Client
	envWebhook *envtest.WebhookInstallOptions
)

func TestMain(m *testing.M) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "test", "crds")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionv1.ValidatingWebhookConfiguration{gateWebhookConfiguration()},
		},
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "starting envtest: %v\n", err)
		os.Exit(1)
	}
	envCfg = cfg
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tinkv1.AddToScheme(scheme))
	envClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building client: %v\n", err)
		os.Exit(1)
	}
	envWebhook = &testEnv.WebhookInstallOptions
	code := m.Run()
	_ = testEnv.Stop()
	os.Exit(code)
}

func newNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "resolve-envtest-"}}
	if err := envClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	return ns.Name
}

// applyAs performs the resolver's sparse operating_system apply under an arbitrary field manager,
// standing in for the retired talos-os-metadata mirror in the coexistence test. It mirrors
// Reconciler.apply's unstructured construction so the managed-fields shape is identical.
func applyAs(ctx context.Context, t *testing.T, manager string, hw *tinkv1.Hardware, p *Plan) {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tinkerbell.org/v1alpha1",
		"kind":       "Hardware",
		"metadata": map[string]any{
			"name":            hw.Name,
			"namespace":       hw.Namespace,
			"resourceVersion": hw.ResourceVersion,
		},
	}}
	osFields := p.OperatingSystem
	if err := unstructured.SetNestedMap(u.Object, map[string]any{
		"slug": osFields.Slug, "distro": osFields.Distro, "version": osFields.Version,
		"image_tag": osFields.ImageTag, "os_slug": osFields.OsSlug,
	}, "spec", "metadata", "instance", "operating_system"); err != nil {
		t.Fatalf("applyAs(%s) build: %v", manager, err)
	}
	if err := envClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(manager), client.ForceOwnership); err != nil {
		t.Fatalf("applyAs(%s): %v", manager, err)
	}
}

// assertOwns re-reads the Hardware and asserts the named field manager owns the given leaf field,
// by scanning that manager's managedFields entry for its `f:<field>` selector.
func assertOwns(ctx context.Context, t *testing.T, obj client.Object, manager, field string) {
	t.Helper()
	got := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
		t.Fatalf("re-getting %s: %v", obj.GetName(), err)
	}
	for _, mf := range got.GetManagedFields() {
		if mf.Manager != manager || mf.FieldsV1 == nil {
			continue
		}
		if strings.Contains(mf.FieldsV1.GetRawString(), "f:"+field) {
			return
		}
	}
	t.Errorf("field manager %q does not own %q", manager, field)
}

func plan(id, version, installer string) *Plan {
	return &Plan{
		OperatingSystem: &tinkv1.MetadataInstanceOperatingSystem{
			Slug: id, Distro: distro, Version: version, ImageTag: version, OsSlug: OSSlug(version, "amd64"),
		},
		InstallerImage: installer,
	}
}

// TestEnvtestResolverOwnsOperatingSystemWithoutTouchingUserData asserts the sparse apply owns
// operating_system while leaving another manager's spec.userData untouched.
func TestEnvtestResolverOwnsOperatingSystemWithoutTouchingUserData(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	// Discovery-style manager authors userData.
	seed := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-1", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	seed.Spec.UserData = ptr.To("#cloud-config machine config")
	if err := envClient.Create(ctx, seed, client.FieldOwner("discovery")); err != nil {
		t.Fatal(err)
	}

	live := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(seed), live); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	if err := r.apply(ctx, live, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9")); err != nil {
		t.Fatal(err)
	}

	got := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(seed), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.UserData == nil || *got.Spec.UserData != "#cloud-config machine config" {
		t.Error("resolver apply damaged another manager's spec.userData")
	}
	if got.Spec.Metadata == nil || got.Spec.Metadata.Instance == nil || got.Spec.Metadata.Instance.OperatingSystem == nil {
		t.Fatal("operating_system was not written")
	}
	osFields := got.Spec.Metadata.Instance.OperatingSystem
	if osFields.Slug != "sid123" || osFields.OsSlug != "talos-v1.13.9-amd64" {
		t.Errorf("operating_system = %+v", osFields)
	}
	if got.Annotations[InstallerImageAnnotation] != "factory.example.test/metal-installer/sid123:v1.13.9" {
		t.Errorf("installer-image annotation = %q", got.Annotations[InstallerImageAnnotation])
	}
	assertOwns(ctx, t, seed, FieldManager, "operating_system")
	assertOwns(ctx, t, seed, "discovery", "userData")
}

// TestEnvtestCoexistenceOwnershipTransfer asserts ForceOwnership pulls operating_system away
// from the retired talos-os-metadata mirror without a conflict.
func TestEnvtestCoexistenceOwnershipTransfer(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()

	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-2", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	if err := envClient.Create(ctx, hw, client.FieldOwner("bootstrap")); err != nil {
		t.Fatal(err)
	}
	// Old C2 mirror owns operating_system first.
	mirror := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	live := &tinkv1.Hardware{}
	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), live)
	applyAs(ctx, t, "talos-os-metadata", live, plan("old", "v1.13.8", "factory.example.test/metal-installer/old:v1.13.8"))

	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), live)
	if err := mirror.apply(ctx, live, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9")); err != nil {
		t.Fatalf("ownership transfer must not conflict: %v", err)
	}
	got := &tinkv1.Hardware{}
	_ = envClient.Get(ctx, client.ObjectKeyFromObject(hw), got)
	if got.Spec.Metadata.Instance.OperatingSystem.Slug != "sid123" {
		t.Error("operating_system value not updated after ownership transfer")
	}
	assertOwns(ctx, t, hw, FieldManager, "operating_system")
}

// TestEnvtestStaleResourceVersionConflicts asserts the optimistic precondition rejects a stale apply.
func TestEnvtestStaleResourceVersionConflicts(t *testing.T) {
	ns := newNamespace(t)
	ctx := context.Background()
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-3", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	if err := envClient.Create(ctx, hw); err != nil {
		t.Fatal(err)
	}
	stale := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(hw), stale); err != nil {
		t.Fatal(err)
	}
	// Bump the object so the captured resourceVersion goes stale.
	stale2 := stale.DeepCopy()
	stale2.Annotations = map[string]string{"x": "y"}
	if err := envClient.Update(ctx, stale2); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	err := r.apply(ctx, stale, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9"))
	if !apierrors.IsConflict(err) {
		t.Fatalf("stale apply err = %v, want Conflict", err)
	}
}
