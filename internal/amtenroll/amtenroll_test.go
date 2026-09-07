package amtenroll

import (
	"context"
	"log/slog"
	"testing"
	"time"

	bmcv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/bmc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		amtv1.AddToScheme,
		bmcv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}
	return s
}

func TestDesiredMachineUsesIntelAMTProvider(t *testing.T) {
	t.Parallel()

	m := DesiredMachine("nuc1", "tink", "10.0.0.160", amt.PortTLS,
		corev1.SecretReference{Name: "nuc1-amt-auth", Namespace: "tink"})

	if m.Spec.Connection.Host != "10.0.0.160" {
		t.Errorf("host = %q", m.Spec.Connection.Host)
	}
	if m.Spec.Connection.Port != amt.PortTLS {
		t.Errorf("port = %d, want %d", m.Spec.Connection.Port, amt.PortTLS)
	}

	opts := m.Spec.Connection.ProviderOptions
	if opts == nil || opts.IntelAMT == nil {
		t.Fatal("IntelAMT provider options are not set")
	}
	if opts.IntelAMT.HostScheme != "https" {
		t.Errorf("hostScheme = %q, want https for the TLS port", opts.IntelAMT.HostScheme)
	}
	// Pinning the order matters: bmclib's default tries ipmitool and gofish
	// first, and an AMT device answers neither, so every action would pay
	// their timeouts before reaching the provider that works.
	if len(opts.PreferredOrder) != 1 || opts.PreferredOrder[0] != "IntelAMT" {
		t.Errorf("preferredOrder = %v, want [IntelAMT]", opts.PreferredOrder)
	}
	// Redfish options must not be set: AMT does not speak Redfish, and
	// leaving them would make bmclib attempt it.
	if opts.Redfish != nil {
		t.Error("Redfish provider options must not be set for an AMT device")
	}
}

func TestDesiredMachinePlaintextPortUsesHTTP(t *testing.T) {
	t.Parallel()

	m := DesiredMachine("nuc1", "tink", "10.0.0.160", amt.PortPlaintext,
		corev1.SecretReference{Name: "s", Namespace: "tink"})
	if got := m.Spec.Connection.ProviderOptions.IntelAMT.HostScheme; got != "http" {
		t.Errorf("hostScheme = %q, want http for port %d", got, amt.PortPlaintext)
	}
}

func TestDesiredAuthSecret(t *testing.T) {
	t.Parallel()

	s := DesiredAuthSecret("nuc1-amt-auth", "tink",
		Credentials{Username: "admin", Password: "new"}, "old")
	if got := string(s.Data[SecretKeyUsername]); got != "admin" {
		t.Errorf("username = %q", got)
	}
	if got := string(s.Data[SecretKeyPassword]); got != "new" {
		t.Errorf("password = %q", got)
	}
	if got := string(s.Data[SecretKeyPreviousPassword]); got != "old" {
		t.Errorf("previousPassword = %q", got)
	}

	// With no previous password the key is absent rather than empty, so a
	// candidate pivot does not try an empty password.
	s = DesiredAuthSecret("nuc1-amt-auth", "tink",
		Credentials{Username: "admin", Password: "new"}, "")
	if _, ok := s.Data[SecretKeyPreviousPassword]; ok {
		t.Error("previousPassword key should be absent when there is no previous password")
	}
}

func TestSetConditionPreservesTransitionTime(t *testing.T) {
	t.Parallel()

	status := &amtv1.AMTDeviceStatus{}
	setCondition(status, amtv1.ConditionReachable, metav1.ConditionTrue, "Reachable", "first")
	if len(status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(status.Conditions))
	}
	first := status.Conditions[0].LastTransitionTime

	time.Sleep(2 * time.Millisecond)
	setCondition(status, amtv1.ConditionReachable, metav1.ConditionTrue, "Reachable", "second")
	if len(status.Conditions) != 1 {
		t.Fatalf("condition was duplicated: %d entries", len(status.Conditions))
	}
	if !status.Conditions[0].LastTransitionTime.Equal(&first) {
		t.Error("LastTransitionTime advanced without a status change")
	}
	if status.Conditions[0].Message != "second" {
		t.Errorf("message = %q, want the updated text", status.Conditions[0].Message)
	}

	time.Sleep(2 * time.Millisecond)
	setCondition(status, amtv1.ConditionReachable, metav1.ConditionFalse, "Gone", "third")
	if status.Conditions[0].LastTransitionTime.Equal(&first) {
		t.Error("LastTransitionTime did not advance on an actual status change")
	}
}

// A steady-state resync must not write. Without this the controller issues a
// status update per device per interval forever.
func TestEqualStatusIgnoresTimestampChurn(t *testing.T) {
	t.Parallel()

	a := &amtv1.AMTDeviceStatus{Phase: amtv1.PhaseRegistered}
	setCondition(a, amtv1.ConditionReachable, metav1.ConditionTrue, "Reachable", "ok")
	a.LastVerifiedTime = &metav1.Time{Time: time.Now()}

	b := a.DeepCopy()
	b.LastVerifiedTime = &metav1.Time{Time: time.Now().Add(time.Hour)}
	b.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now().Add(time.Hour))

	if !equalStatus(a, b) {
		t.Error("statuses differing only by timestamps should compare equal")
	}

	c := b.DeepCopy()
	c.Phase = amtv1.PhaseFailed
	if equalStatus(a, c) {
		t.Error("a phase change must not compare equal")
	}
}

func TestApplyFactsAndInventory(t *testing.T) {
	t.Parallel()

	conn := &verified{
		fingerprint: "abc123",
		facts: &amt.Facts{
			PlatformGUID:        "1b72a5ce-4584-9e6a-9f12-88aedd753da0",
			ProvisioningState:   amt.ProvisioningPost,
			ControlMode:         amt.ControlModeAdmin,
			AllowedControlModes: []string{amt.ControlModeAdmin, amt.ControlModeClient},
			DigestRealm:         "Digest:AB",
			Firmware:            amt.Firmware{AMT: "18.1.18", Build: "2635", SKU: "16392"},
			BootCapabilities:    amt.BootCapabilities{PXE: true, UEFIHTTPS: true, IDER: true},
			Redirection:         amt.Redirection{EnabledState: amt.RedirectionIDERAndSOL},
		},
	}

	status := &amtv1.AMTDeviceStatus{}
	applyFacts(status, conn)

	if status.TLSFingerprint != "abc123" {
		t.Errorf("fingerprint = %q", status.TLSFingerprint)
	}
	if status.ControlMode != amt.ControlModeAdmin {
		t.Errorf("controlMode = %q", status.ControlMode)
	}
	if !status.BootCapabilities.UEFIHTTPS {
		t.Error("uefiHTTPS capability was not carried into status")
	}
	if status.Redirection.EnabledState != amt.RedirectionIDERAndSOL {
		t.Errorf("redirection enabledState = %d", status.Redirection.EnabledState)
	}
	if status.DigestRealm != "Digest:AB" {
		t.Error("digest realm must be recorded; password rotation is impossible without it")
	}

	inv := &amt.Inventory{Summary: amt.Summary{
		Manufacturer: "ASUSTeK COMPUTER INC.",
		Model:        "NUC15CRHV7",
		SerialNumber: "TBARQK0038327AB",
		MemoryBytes:  103079215104,
		CPUCount:     1,
		NICs:         []amt.NIC{{MACAddress: "88:ae:dd:75:3d:a0", Name: "Wired0"}},
	}}
	applyInventory(status, inv)

	if status.Inventory == nil {
		t.Fatal("inventory was not recorded")
	}
	if status.Inventory.Model != "NUC15CRHV7" {
		t.Errorf("model = %q", status.Inventory.Model)
	}
	if len(status.Inventory.NICs) != 1 || status.Inventory.NICs[0].MACAddress != "88:ae:dd:75:3d:a0" {
		t.Errorf("nics = %+v", status.Inventory.NICs)
	}
}

// The per-device Secret must be tried before profile-wide candidates, and its
// previousPassword before them too: that ordering is what lets a half-failed
// rotation recover instead of falling back to a stale fleet credential.
func TestCandidateOrdering(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"},
		Spec: amtv1.AMTDeviceSpec{
			CredentialsRef: &corev1.LocalObjectReference{Name: "nuc1-amt-auth"},
		},
	}
	deviceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1-amt-auth", Namespace: "tink"},
		Data: map[string][]byte{
			SecretKeyUsername:         []byte("admin"),
			SecretKeyPassword:         []byte("current"),
			SecretKeyPreviousPassword: []byte("previous"),
		},
	}
	fleetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "tink"},
		Data: map[string][]byte{
			SecretKeyUsername: []byte("admin"),
			SecretKeyPassword: []byte("fleet"),
		},
	}
	profile := &amtv1.AMTProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: amtv1.AMTProfileSpec{
			CredentialSources: []corev1.SecretReference{{Name: "fleet", Namespace: "tink"}},
		},
	}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(device, deviceSecret, fleetSecret, profile).Build(),
		Log: slog.Default(),
	}

	got, err := r.candidates(context.Background(), device, profile)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}

	want := []Credentials{
		{Username: "admin", Password: "current"},
		{Username: "admin", Password: "previous"},
		{Username: "admin", Password: "fleet"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCandidatesDeduplicates(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"},
		Spec: amtv1.AMTDeviceSpec{
			CredentialsRef: &corev1.LocalObjectReference{Name: "nuc1-amt-auth"},
		},
	}
	same := map[string][]byte{
		SecretKeyUsername: []byte("admin"),
		SecretKeyPassword: []byte("same"),
	}
	profile := &amtv1.AMTProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: amtv1.AMTProfileSpec{
			CredentialSources: []corev1.SecretReference{{Name: "fleet", Namespace: "tink"}},
		},
	}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			device,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "nuc1-amt-auth", Namespace: "tink"}, Data: same},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "tink"}, Data: same},
			profile,
		).Build(),
		Log: slog.Default(),
	}

	got, err := r.candidates(context.Background(), device, profile)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("identical credentials should be tried once, got %d: %+v", len(got), got)
	}
}

// A missing per-device Secret must fall through to profile candidates rather
// than failing: that path is exactly how a device with a lost Secret recovers.
func TestCandidatesToleratesMissingDeviceSecret(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"},
		Spec: amtv1.AMTDeviceSpec{
			CredentialsRef: &corev1.LocalObjectReference{Name: "gone"},
		},
	}
	profile := &amtv1.AMTProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: amtv1.AMTProfileSpec{
			CredentialSources: []corev1.SecretReference{{Name: "fleet", Namespace: "tink"}},
		},
	}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			device, profile,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "tink"},
				Data: map[string][]byte{
					SecretKeyUsername: []byte("admin"),
					SecretKeyPassword: []byte("fleet"),
				},
			},
		).Build(),
		Log: slog.Default(),
	}

	got, err := r.candidates(context.Background(), device, profile)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(got) != 1 || got[0].Password != "fleet" {
		t.Errorf("got %+v, want the fleet credential", got)
	}
}

func TestCandidatesEmptyIsAnError(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"}}
	profile := &amtv1.AMTProfile{ObjectMeta: metav1.ObjectMeta{Name: "default"}}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(device, profile).Build(),
		Log:    slog.Default(),
	}

	if _, err := r.candidates(context.Background(), device, profile); err == nil {
		t.Fatal("expected an error when no candidates exist")
	}
}

// A missing default profile must not make every device unmanageable.
func TestProfileForMissingDefaultIsEmptyPolicy(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"}}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(device).Build(),
		Log:    slog.Default(),
	}
	r.applyDefaults()

	profile, err := r.profileFor(context.Background(), device)
	if err != nil {
		t.Fatalf("profileFor: %v", err)
	}
	if profile == nil {
		t.Fatal("expected an empty profile, got nil")
	}
}

// A device naming a profile that does not exist is a configuration error and
// must surface, unlike the absent default.
func TestProfileForMissingNamedProfileIsAnError(t *testing.T) {
	t.Parallel()

	scheme := testScheme(t)
	device := &amtv1.AMTDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "nuc1", Namespace: "tink"},
		Spec:       amtv1.AMTDeviceSpec{ProfileRef: corev1.LocalObjectReference{Name: "nope"}},
	}

	r := &Reconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(device).Build(),
		Log:    slog.Default(),
	}
	r.applyDefaults()

	if _, err := r.profileFor(context.Background(), device); err == nil {
		t.Fatal("expected an error for a named profile that does not exist")
	}
}
