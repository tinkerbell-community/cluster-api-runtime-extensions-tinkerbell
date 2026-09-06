package resolve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterUploadsOnceAndCaches(t *testing.T) {
	t.Parallel()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/schematics" || r.Method != http.MethodPost {
			t.Errorf("got %s %s, want POST /schematics", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "abc123"})
	}))
	defer server.Close()

	registrar := NewRegistrar(server.URL)
	doc := Build(Signals{Architecture: archAMD64, DiskDevices: []string{"/dev/nvme0n1"}})
	for range 3 {
		id, err := registrar.Register(context.Background(), doc)
		if err != nil {
			t.Fatal(err)
		}
		if id != "abc123" {
			t.Fatalf("id = %q, want abc123", id)
		}
	}
	if calls != 1 {
		t.Errorf("uploaded %d times, want 1 — steady-state reconciles must not call the factory", calls)
	}
}

func TestRegisterReportsFactoryErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid schematic"))
	}))
	defer server.Close()
	if _, err := NewRegistrar(server.URL).Register(context.Background(), Build(Signals{})); err == nil {
		t.Fatal("expected an error when the factory rejects the schematic")
	}
}
