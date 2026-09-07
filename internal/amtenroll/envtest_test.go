//go:build envtest

package amtenroll

// Server-side-apply field ownership is only trustworthy against a real API
// server -- the fake client's SSA emulation has diverged from apiserver
// behaviour historically -- so the ownership guarantees this component depends
// on are asserted here rather than in the unit tests.
//
// Run with: make test-envtest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	bmcv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/bmc"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
)

var envClient client.Client

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

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme, bmcv1.AddToScheme, tinkv1.AddToScheme, amtv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			fmt.Fprintf(os.Stderr, "building scheme: %v\n", err)
			os.Exit(1)
		}
	}
	envClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "building client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := testEnv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stopping envtest: %v\n", err)
	}
	os.Exit(code)
}

func envNamespace(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("amt-%d", time.Now().UnixNano())
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := envClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	return name
}

func testApplier(t *testing.T) *applier {
	t.Helper()
	return &applier{
		client: envClient,
		now:    time.Now,
		log:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// The CRDs must actually install and accept the objects the controller writes.
// A schema mistake here is invisible to unit tests using a fake client.
func TestEnvtestCRDsAcceptResources(t *testing.T) {
	ctx := context.Background()
	ns := envNamespace(t)

	profile := &amtv1.AMTProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "envtest-" + ns},
		Spec: amtv1.AMTProfileSpec{
			ControlMode: amtv1.ControlModeBest,
			Consent:     amtv1.OptInNone,
			CredentialSources: []corev1.SecretReference{
				{Name: "amt-credentials", Namespace: ns},
			},
			Password: amtv1.PasswordPolicy{Length: 24},
			TLS:      amtv1.TLSPolicy{Mode: amtv1.TLSModePin},
			Redirection: amtv1.RedirectionPolicy{
				SOL:             ptr.To(true),
				ListenerEnabled: ptr.To(false),
			},
			VirtualMedia: amtv1.VirtualMediaPolicy{
				Enabled:      ptr.To(true),
				ImageBaseURL: "https://images.example.com",
			},
		},
	}
	if err := envClient.Create(ctx, profile); err != nil {
		t.Fatalf("creating AMTProfile: %v", err)
	}
	t.Cleanup(func() { _ = envClient.Delete(ctx, profile) })

	device := &amtv1.AMTDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: ns},
		Spec: amtv1.AMTDeviceSpec{
			Endpoint: amtv1.Endpoint{Host: "10.0.0.160", Port: 16993, Scheme: "https"},
			Identity: amtv1.Identity{
				PlatformGUID: "1b72a5ce-4584-9e6a-9f12-88aedd753da0",
				MACAddresses: []string{"88:ae:dd:75:3d:a0"},
			},
		},
	}
	if err := envClient.Create(ctx, device); err != nil {
		t.Fatalf("creating AMTDevice: %v", err)
	}

	// Status is a subresource, so it must be written through Status().
	device.Status.Phase = amtv1.PhaseRegistered
	device.Status.ControlMode = "Admin"
	device.Status.AllowedControlModes = []string{"Admin", "Client"}
	device.Status.Firmware = amtv1.FirmwareInfo{AMT: "18.1.18", Build: "2635"}
	device.Status.BootCapabilities = amtv1.BootCapabilities{PXE: true, UEFIHTTPS: true}
	device.Status.Inventory = &amtv1.InventorySummary{
		Model:       "NUC15CRHV7",
		MemoryBytes: 103079215104,
		NICs:        []amtv1.NIC{{MACAddress: "88:ae:dd:75:3d:a0", Name: "Wired0"}},
	}
	setCondition(&device.Status, amtv1.ConditionReachable, metav1.ConditionTrue, "Reachable", "ok")
	if err := envClient.Status().Update(ctx, device); err != nil {
		t.Fatalf("updating AMTDevice status: %v", err)
	}

	fetched := &amtv1.AMTDevice{}
	key := client.ObjectKey{Namespace: ns, Name: "nuc1"}
	if err := envClient.Get(ctx, key, fetched); err != nil {
		t.Fatalf("reading back AMTDevice: %v", err)
	}
	if fetched.Status.Inventory == nil || fetched.Status.Inventory.MemoryBytes != 103079215104 {
		t.Errorf("inventory did not round-trip: %+v", fetched.Status.Inventory)
	}
	if len(fetched.Status.Conditions) != 1 {
		t.Errorf("conditions did not round-trip: %+v", fetched.Status.Conditions)
	}
}

// A resource without this component's managed-by label must never be modified.
// Hand-provisioned Machines and Hardware, and anything belonging to the mDNS
// discovery controller, have to survive untouched.
func TestEnvtestSkipsUnmanagedResources(t *testing.T) {
	ctx := context.Background()
	ns := envNamespace(t)
	app := testApplier(t)

	foreign := &bmcv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foreign",
			Namespace: ns,
			Labels:    map[string]string{"discovery.tinkerbell.org/managed-by": "someone-else"},
		},
		Spec: bmcv1.MachineSpec{
			Connection: bmcv1.Connection{
				Host:          "10.0.0.99",
				Port:          623,
				AuthSecretRef: corev1.SecretReference{Name: "other", Namespace: ns},
			},
		},
	}
	if err := envClient.Create(ctx, foreign); err != nil {
		t.Fatalf("creating foreign machine: %v", err)
	}

	desired := DesiredMachine("foreign", ns, "10.0.0.160", 16993,
		corev1.SecretReference{Name: "nuc1-amt-auth", Namespace: ns})
	if err := app.apply(ctx, "machine", &bmcv1.Machine{}, desired, ownership{deviceName: "nuc1"}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	after := &bmcv1.Machine{}
	if err := envClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "foreign"}, after); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Spec.Connection.Host != "10.0.0.99" {
		t.Errorf("an unmanaged Machine was modified: host = %q", after.Spec.Connection.Host)
	}
	if after.Spec.Connection.ProviderOptions != nil {
		t.Error("an unmanaged Machine gained provider options")
	}
}

// The apply must own only the fields it serializes. A foreign writer's fields
// on the same object have to survive, which is the whole point of the sparse
// apply discipline.
func TestEnvtestApplyDoesNotClobberForeignFields(t *testing.T) {
	ctx := context.Background()
	ns := envNamespace(t)
	app := testApplier(t)

	own := ownership{platformGUID: "guid-1", deviceName: "nuc1"}
	desired := DesiredMachine("nuc1", ns, "10.0.0.160", 16993,
		corev1.SecretReference{Name: "nuc1-amt-auth", Namespace: ns})
	if err := app.apply(ctx, "machine", &bmcv1.Machine{}, desired, own, nil); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	key := client.ObjectKey{Namespace: ns, Name: "nuc1"}

	// A different manager asserts a field this component never serializes.
	// The patch is unstructured and carries only that field: a typed object
	// would serialize its zero-valued spec and assert an empty required host,
	// which is the same trap applyConfigurationFor exists to avoid.
	foreign := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": bmcv1.GroupVersion.String(),
		"kind":       "Machine",
		"metadata": map[string]any{
			"name":      "nuc1",
			"namespace": ns,
			"annotations": map[string]any{
				"foreign.example.com/note": "set by someone else",
			},
		},
	}}
	if err := envClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(foreign),
		client.FieldOwner("foreign-manager"), client.ForceOwnership); err != nil {
		t.Fatalf("foreign apply: %v", err)
	}

	// Re-apply and confirm the foreign annotation survives.
	desired2 := DesiredMachine("nuc1", ns, "10.0.0.160", 16993,
		corev1.SecretReference{Name: "nuc1-amt-auth", Namespace: ns})
	if err := app.apply(ctx, "machine", &bmcv1.Machine{}, desired2, own, nil); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	after := &bmcv1.Machine{}
	if err := envClient.Get(ctx, key, after); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if after.Annotations["foreign.example.com/note"] == "" {
		t.Error("a foreign annotation was removed by this component's apply")
	}
	if after.Annotations[PlatformGUIDAnnotation] != "guid-1" {
		t.Errorf("this component's own annotation is missing: %v", after.Annotations)
	}
	if after.Spec.Connection.ProviderOptions == nil ||
		after.Spec.Connection.ProviderOptions.IntelAMT == nil {
		t.Error("IntelAMT provider options were not applied")
	}
}

// Creating is a plain POST rather than an apply-as-upsert, so racing a
// concurrent foreign creation fails loudly instead of silently adopting it.
func TestEnvtestCreateIsNotAdoption(t *testing.T) {
	ctx := context.Background()
	ns := envNamespace(t)
	app := testApplier(t)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1-amt-auth", Namespace: ns},
		Data:       map[string][]byte{"username": []byte("someone")},
	}
	if err := envClient.Create(ctx, secret); err != nil {
		t.Fatalf("creating foreign secret: %v", err)
	}

	desired := DesiredAuthSecret("nuc1-amt-auth", ns,
		Credentials{Username: "admin", Password: "hunter2"}, "")
	if err := app.apply(ctx, "secret", &corev1.Secret{}, desired,
		ownership{deviceName: "nuc1"}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	after := &corev1.Secret{}
	if err := envClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "nuc1-amt-auth"}, after); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(after.Data["username"]) != "someone" {
		t.Errorf("an unmanaged Secret was overwritten: %q", after.Data["username"])
	}
}

// A managed resource is updated in place, and the credential rotation fields
// round-trip.
func TestEnvtestManagedSecretIsUpdated(t *testing.T) {
	ctx := context.Background()
	ns := envNamespace(t)
	app := testApplier(t)
	own := ownership{deviceName: "nuc2"}

	first := DesiredAuthSecret("nuc2-amt-auth", ns,
		Credentials{Username: "admin", Password: "old"}, "")
	if err := app.apply(ctx, "secret", &corev1.Secret{}, first, own, nil); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	second := DesiredAuthSecret("nuc2-amt-auth", ns,
		Credentials{Username: "admin", Password: "new"}, "old")
	if err := app.apply(ctx, "secret", &corev1.Secret{}, second, own, nil); err != nil {
		t.Fatalf("second apply: %v", err)
	}

	after := &corev1.Secret{}
	if err := envClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "nuc2-amt-auth"}, after); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if got := string(after.Data[SecretKeyPassword]); got != "new" {
		t.Errorf("password = %q, want new", got)
	}
	if got := string(after.Data[SecretKeyPreviousPassword]); got != "old" {
		t.Errorf("previousPassword = %q, want old; a half-failed rotation could not recover", got)
	}
}
