package amt

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cfg     Config
		wantErr error
	}{
		"defaults to TLS":  {cfg: Config{Host: "10.0.0.1"}},
		"explicit TLS":     {cfg: Config{Host: "10.0.0.1", Port: PortTLS}},
		"explicit plain":   {cfg: Config{Host: "10.0.0.1", Port: PortPlaintext}},
		"missing host":     {cfg: Config{}, wantErr: errors.New("host")},
		"unsupported port": {cfg: Config{Host: "10.0.0.1", Port: 443}, wantErr: ErrUnsupportedPort},
		"redirection port": {cfg: Config{Host: "10.0.0.1", Port: 16995}, wantErr: ErrUnsupportedPort},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.validate()
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr == nil:
				return
			case err == nil:
				t.Fatalf("expected an error, got none")
			case errors.Is(tc.wantErr, ErrUnsupportedPort) && !errors.Is(err, ErrUnsupportedPort):
				t.Fatalf("expected ErrUnsupportedPort, got %v", err)
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{Host: "h"}
	if got := cfg.port(); got != PortTLS {
		t.Errorf("port() = %d, want %d", got, PortTLS)
	}
	if !cfg.useTLS() {
		t.Error("useTLS() = false, want true for the default port")
	}
	if got := cfg.timeout(); got != DefaultTimeout {
		t.Errorf("timeout() = %v, want %v", got, DefaultTimeout)
	}

	plain := Config{Host: "h", Port: PortPlaintext}
	if plain.useTLS() {
		t.Error("useTLS() = true for port 16992, want false")
	}
}

func TestGeneratePassword(t *testing.T) {
	t.Parallel()

	for _, length := range []int{8, 12, 24, 32} {
		for i := 0; i < 50; i++ {
			pw, err := GeneratePassword(length)
			if err != nil {
				t.Fatalf("GeneratePassword(%d): %v", length, err)
			}
			if len(pw) != length {
				t.Fatalf("GeneratePassword(%d) returned %d characters", length, len(pw))
			}
			if err := ValidatePassword(pw); err != nil {
				t.Fatalf("generated password %q fails validation: %v", pw, err)
			}
		}
	}
}

func TestGeneratePasswordRejectsBadLength(t *testing.T) {
	t.Parallel()

	for _, length := range []int{0, 7, 33, 100, -1} {
		if _, err := GeneratePassword(length); !errors.Is(err, ErrPasswordPolicy) {
			t.Errorf("GeneratePassword(%d) error = %v, want ErrPasswordPolicy", length, err)
		}
	}
}

// Generated passwords must never contain characters some AMT firmware
// rejects. A rejected password is written before it is discovered to be
// invalid, which locks the controller out of the device.
func TestGeneratePasswordAvoidsRiskyCharacters(t *testing.T) {
	t.Parallel()

	const risky = `:,"'\` + "`"
	for i := 0; i < 500; i++ {
		pw, err := GeneratePassword(32)
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if strings.ContainsAny(pw, risky) {
			t.Fatalf("password %q contains a character outside the safe set", pw)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pw   string
		want bool
	}{
		"valid":            {"Abcdef1!", true},
		"too short":        {"Abc1!", false},
		"no upper":         {"abcdef1!", false},
		"no lower":         {"ABCDEF1!", false},
		"no digit":         {"Abcdefg!", false},
		"no special":       {"Abcdefg1", false},
		"illegal colon":    {"Abcdef1:", false},
		"illegal quote":    {`Abcdef1"`, false},
		"illegal comma":    {"Abcdef1,", false},
		"too long":         {strings.Repeat("Ab1!", 9), false},
		"max length valid": {strings.Repeat("Ab1!", 8), true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePassword(tc.pw)
			if tc.want && err != nil {
				t.Errorf("ValidatePassword(%q) = %v, want nil", tc.pw, err)
			}
			if !tc.want && err == nil {
				t.Errorf("ValidatePassword(%q) = nil, want an error", tc.pw)
			}
		})
	}
}

// The digest is what actually reaches the device, so a regression here silently
// sets a password nobody can use. Vector computed independently:
// md5("admin:Digest:AB:hunter2").
func TestDigestPassword(t *testing.T) {
	t.Parallel()

	// Vector computed independently: printf 'admin:Digest:AB:hunter2' | md5sum
	const want = "172bdd4d350a6568d29e715bf3b00a02"

	got := DigestPassword("admin", "Digest:AB", "hunter2")
	if got != want {
		t.Fatalf("DigestPassword = %q, want %q", got, want)
	}
	// Recompute the same way a caller would to guard the field order, which is
	// the part that actually goes wrong.
	if again := DigestPassword("admin", "Digest:AB", "hunter2"); again != got {
		t.Fatal("DigestPassword is not deterministic")
	}
	if same := DigestPassword("admin", "Digest:AB", "hunter3"); same == got {
		t.Fatal("DigestPassword ignores the password")
	}
	if same := DigestPassword("admin", "Digest:AC", "hunter2"); same == got {
		t.Fatal("DigestPassword ignores the realm")
	}
	if same := DigestPassword("root", "Digest:AB", "hunter2"); same == got {
		t.Fatal("DigestPassword ignores the username")
	}
}

func TestNormalizeMAC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare hex as AMT reports it", "D83ADDC98C28", "d8:3a:dd:c9:8c:28"},
		{"already colon separated", "d8:3a:dd:c9:8c:28", "d8:3a:dd:c9:8c:28"},
		{"dash separated", "D8-3A-DD-C9-8C-28", "d8:3a:dd:c9:8c:28"},
		{"surrounding whitespace", " d83addc98c28 ", "d8:3a:dd:c9:8c:28"},
		{"empty", "", ""},
		{"not hex", "not-a-mac", ""},
		{"too short", "D83ADDC98C2", ""},
		{"non hex digits", "ZZ3ADDC98C28", ""},
		{"too long", "00000000000000", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeMAC(tc.in); got != tc.want {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsZeroMAC(t *testing.T) {
	t.Parallel()

	if !isZeroMAC("00:00:00:00:00:00") {
		t.Error("all-zero MAC should be reported as zero")
	}
	for _, mac := range []string{"88:ae:dd:75:3d:a0", "00:00:00:00:00:01", ""} {
		if isZeroMAC(mac) {
			t.Errorf("isZeroMAC(%q) = true, want false", mac)
		}
	}
}

func TestNormalizeGUID(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"1B72A5CE45849E6A9F1288AEDD753DA0": "1b72a5ce-4584-9e6a-9f12-88aedd753da0",
		"1b72a5ce45849e6a9f1288aedd753da0": "1b72a5ce-4584-9e6a-9f12-88aedd753da0",
		"short":                            "short",
		"":                                 "",
	}

	for in, want := range tests {
		if got := normalizeGUID(in); got != want {
			t.Errorf("normalizeGUID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFactsHelpers(t *testing.T) {
	t.Parallel()

	provisioned := Facts{ProvisioningState: ProvisioningPost}
	if !provisioned.Provisioned() {
		t.Error("PostProvisioning should report Provisioned")
	}
	for _, state := range []string{ProvisioningPre, ProvisioningIn, "Unknown(9)"} {
		if (Facts{ProvisioningState: state}).Provisioned() {
			t.Errorf("%q should not report Provisioned", state)
		}
	}

	if !(Facts{AllowedControlModes: []string{ControlModeAdmin, ControlModeClient}}).AllowsAdminControlMode() {
		t.Error("Admin in AllowedControlModes should report true")
	}
	if (Facts{AllowedControlModes: []string{ControlModeClient}}).AllowsAdminControlMode() {
		t.Error("Client-only should report false")
	}
	if (Facts{}).AllowsAdminControlMode() {
		t.Error("empty AllowedControlModes should report false")
	}
}

func TestControlModeAndProvisioningStrings(t *testing.T) {
	t.Parallel()

	if got := controlModeString(2); got != ControlModeAdmin {
		t.Errorf("controlModeString(2) = %q, want %q", got, ControlModeAdmin)
	}
	if got := controlModeString(1); got != ControlModeClient {
		t.Errorf("controlModeString(1) = %q, want %q", got, ControlModeClient)
	}
	if got := controlModeString(0); got != ControlModeNotProvisioned {
		t.Errorf("controlModeString(0) = %q, want %q", got, ControlModeNotProvisioned)
	}
	if got := controlModeString(9); !strings.HasPrefix(got, "Unknown") {
		t.Errorf("controlModeString(9) = %q, want an Unknown form", got)
	}

	if got := provisioningStateString(2); got != ProvisioningPost {
		t.Errorf("provisioningStateString(2) = %q, want %q", got, ProvisioningPost)
	}
	if got := provisioningStateString(0); got != ProvisioningPre {
		t.Errorf("provisioningStateString(0) = %q, want %q", got, ProvisioningPre)
	}
}

func TestValidateImageURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"https://images.example.com/hook.iso",
		"https://10.0.0.10:8443/a/b.iso",
	}
	for _, u := range valid {
		if err := validateImageURL(u); err != nil {
			t.Errorf("validateImageURL(%q) = %v, want nil", u, err)
		}
	}

	// http is the important rejection: AMT only performs HTTPS boot, and every
	// call in the sequence succeeds before the device silently boots normally.
	invalid := []string{
		"",
		"http://images.example.com/hook.iso",
		"ftp://images.example.com/hook.iso",
		"https://",
		"/relative/path.iso",
	}
	for _, u := range invalid {
		if err := validateImageURL(u); err == nil {
			t.Errorf("validateImageURL(%q) = nil, want an error", u)
		}
	}
}

func TestSupportedResetTypesMapCompletely(t *testing.T) {
	t.Parallel()

	for _, rt := range SupportedResetTypes() {
		if _, ok := resetTypes[rt]; !ok {
			t.Errorf("SupportedResetTypes advertises %q with no CIM mapping", rt)
		}
	}
	if len(SupportedResetTypes()) != len(resetTypes) {
		t.Errorf("SupportedResetTypes lists %d types but %d are mapped",
			len(SupportedResetTypes()), len(resetTypes))
	}
}

func TestBootTargetsMapped(t *testing.T) {
	t.Parallel()

	for _, target := range []BootTarget{BootPxe, BootHdd, BootCd, BootUefiHTTP} {
		if _, ok := bootSources[target]; !ok {
			t.Errorf("boot target %q has no CIM source mapping", target)
		}
	}
	// BiosSetup is handled through BootSettingData rather than a boot source.
	if _, ok := bootSources[BootBiosSetup]; ok {
		t.Error("BiosSetup should not have a boot source mapping")
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	// SHA-256 of the empty input, hex encoded.
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Fingerprint(nil); got != emptySHA256 {
		t.Errorf("Fingerprint(nil) = %q, want %q", got, emptySHA256)
	}
	if a, b := Fingerprint([]byte("a")), Fingerprint([]byte("b")); a == b {
		t.Error("Fingerprint collides on different input")
	}
}
