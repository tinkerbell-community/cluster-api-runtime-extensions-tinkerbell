package resolve

import (
	"strings"
	"testing"
)

// §3.3: os_slug is the DASH form talos-<version>-<arch>; the underscore fallback must
// NEVER be produced, and arch is always the final dash segment even for pre-releases.
func TestOSSlugIsDashFormNeverUnderscore(t *testing.T) {
	t.Parallel()
	got := OSSlug("v1.13.9", "amd64")
	if got != "talos-v1.13.9-amd64" {
		t.Fatalf("OSSlug = %q, want talos-v1.13.9-amd64", got)
	}
	if strings.Contains(got, "_") || strings.Contains(got, "talos_v1_13_9") {
		t.Fatalf("OSSlug produced the dead underscore form: %q", got)
	}
}

func TestOSSlugArchIsLastDashSegmentForPreRelease(t *testing.T) {
	t.Parallel()
	got := OSSlug("v1.14.0-rc.1", "amd64")
	parts := strings.Split(got, "-")
	if last := parts[len(parts)-1]; last != "amd64" {
		t.Fatalf("splitList last of %q = %q, want amd64", got, last)
	}
}

func TestTootlesUserDataURLFromIP(t *testing.T) {
	t.Parallel()
	got, err := TootlesUserDataURL("", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://10.0.0.1:7080/2009-04-04/user-data" {
		t.Fatalf("url = %q", got)
	}
}

func TestTootlesUserDataURLRequiresOneSource(t *testing.T) {
	t.Parallel()
	if _, err := TootlesUserDataURL("", ""); err == nil {
		t.Fatal("expected an error when neither --tootles-url nor --tinkerbell-ip is set")
	}
}
