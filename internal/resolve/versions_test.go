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

func TestStaleCacheServedOnRefreshError(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			_ = json.NewEncoder(w).Encode(realisticVersions())
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewVersionResolver(srv.URL)

	// Warm the cache with the one good response the server ever gives.
	patch, err := r.LatestPatch(context.Background(), "v1.13.9")
	if err != nil {
		t.Fatal(err)
	}
	if patch != "v1.13.10" {
		t.Fatalf("warm LatestPatch = %q, want v1.13.10", patch)
	}
	minor, err := r.LatestMinor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if minor != "v1.14" {
		t.Fatalf("warm LatestMinor = %q, want v1.14", minor)
	}

	// Force the next access to treat the cache as expired, without waiting out the real
	// 10-minute TTL, so it re-fetches and hits the server's 500.
	r.mu.Lock()
	r.ttl = 0
	r.mu.Unlock()

	patch, err = r.LatestPatch(context.Background(), "v1.13.9")
	if err != nil {
		t.Errorf("LatestPatch after failed refresh returned error %v, want nil (stale cache served)", err)
	}
	if patch != "v1.13.10" {
		t.Errorf("LatestPatch after failed refresh = %q, want v1.13.10 (stale cache)", patch)
	}

	minor, err = r.LatestMinor(context.Background())
	if err != nil {
		t.Errorf("LatestMinor after failed refresh returned error %v, want nil (stale cache served)", err)
	}
	if minor != "v1.14" {
		t.Errorf("LatestMinor after failed refresh = %q, want v1.14 (stale cache)", minor)
	}

	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Errorf("factory hit %d times, want >= 2 (warm fetch + at least one failed refresh)", got)
	}
}
