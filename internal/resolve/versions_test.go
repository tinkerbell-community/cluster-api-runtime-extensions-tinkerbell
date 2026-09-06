package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func realisticVersions() []string {
	return []string{
		"v1.12.10", "v1.12.11", "v1.12.12",
		"v1.13.0", "v1.13.2", "v1.13.9", "v1.13.10",
		"v1.14.0-alpha.0", "v1.14.0-beta.1", "v1.14.0-rc.2", "v1.14.0",
	}
}

func versionsServer(t *testing.T, hits *int32, versions []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		_ = json.NewEncoder(w).Encode(versions)
	}))
}

func TestLatestPatchReturnsNewestGAInMinor(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.13.10" {
		t.Errorf("LatestPatch(v1.13.9) = %q, want v1.13.10", got)
	}
}

func TestLatestPatchSkipsPreReleases(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.14")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.14.0" {
		t.Errorf("LatestPatch(v1.14) = %q, want v1.14.0 (pre-releases skipped)", got)
	}
}

func TestLatestPatchUnknownMinorReturnsEmpty(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, realisticVersions())
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestPatch(context.Background(), "v1.99")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("LatestPatch(v1.99) = %q, want empty", got)
	}
}

func TestLatestMinorIgnoresMinorWithOnlyPreReleases(t *testing.T) {
	t.Parallel()
	srv := versionsServer(t, nil, []string{"v1.13.9", "v1.13.10", "v1.14.0-alpha.0", "v1.14.0-rc.2"})
	defer srv.Close()
	got, err := NewVersionResolver(srv.URL).LatestMinor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.13" {
		t.Errorf("LatestMinor() = %q, want v1.13", got)
	}
}

func TestVersionsAreCachedWithinTTL(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := versionsServer(t, &hits, realisticVersions())
	defer srv.Close()
	r := NewVersionResolver(srv.URL)
	for range 3 {
		if _, err := r.LatestPatch(context.Background(), "v1.13"); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Errorf("factory hit %d times, want 1 (cached within TTL)", hits)
	}
}
