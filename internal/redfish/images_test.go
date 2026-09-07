package redfish

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func imageServer(t *testing.T) (*ImageServer, *httptest.Server) {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hook.iso"), []byte("ISO-CONTENT"), 0o600); err != nil {
		t.Fatalf("writing test image: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatalf("creating nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "secret.iso"), []byte("NO"), 0o600); err != nil {
		t.Fatalf("writing nested image: %v", err)
	}

	s := &ImageServer{
		Root:    root,
		BaseURL: "https://images.example.com",
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	mux := http.NewServeMux()
	mux.Handle(ImagePrefix, s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func TestImageServerServesImage(t *testing.T) {
	t.Parallel()
	_, srv := imageServer(t)

	resp, err := srv.Client().Get(srv.URL + ImagePrefix + "hook.iso")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ISO-CONTENT" {
		t.Errorf("body = %q", body)
	}
}

// Firmware HTTPS boot commonly fetches an image in ranges rather than as one
// stream, so range support is not optional here.
func TestImageServerSupportsRangeRequests(t *testing.T) {
	t.Parallel()
	_, srv := imageServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+ImagePrefix+"hook.iso", nil)
	req.Header.Set("Range", "bytes=0-2")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ISO" {
		t.Errorf("range body = %q, want %q", body, "ISO")
	}
}

// Only a flat set of images is exposed. Anything with a separator is refused
// outright rather than cleaned, so no traversal can be built from an encoding
// this code did not anticipate.
func TestImageServerRefusesTraversalAndNesting(t *testing.T) {
	t.Parallel()
	_, srv := imageServer(t)

	paths := []string{
		ImagePrefix + "nested/secret.iso",
		ImagePrefix + "../images.go",
		ImagePrefix + "..%2f..%2fetc%2fpasswd",
		ImagePrefix + ".",
		ImagePrefix + "..",
		ImagePrefix,
	}
	for _, p := range paths {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s = 200; it should not be served", p)
		}
	}
}

func TestImageServerRejectsWriteMethods(t *testing.T) {
	t.Parallel()
	_, srv := imageServer(t)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, srv.URL+ImagePrefix+"hook.iso", nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", method, resp.StatusCode)
		}
	}
}

func TestImageServerURLFor(t *testing.T) {
	t.Parallel()
	s, _ := imageServer(t)

	got, err := s.URLFor("hook.iso")
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}
	if want := "https://images.example.com" + ImagePrefix + "hook.iso"; got != want {
		t.Errorf("URLFor = %q, want %q", got, want)
	}

	// A path is reduced to its base name so a caller cannot smuggle a
	// directory into the URL handed to a device.
	got, err = s.URLFor("/some/where/hook.iso")
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}
	if want := "https://images.example.com" + ImagePrefix + "hook.iso"; got != want {
		t.Errorf("URLFor with a path = %q, want %q", got, want)
	}

	if _, err := s.URLFor(""); err == nil {
		t.Error("URLFor(\"\") should be an error")
	}
}

// A misconfigured image server produces a boot that succeeds at every step and
// then silently does not happen, so it must fail at startup instead.
func TestImageServerValidate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	tests := map[string]struct {
		server  ImageServer
		wantErr bool
	}{
		"valid":         {ImageServer{Root: root, BaseURL: "https://x.example.com"}, false},
		"no root":       {ImageServer{BaseURL: "https://x.example.com"}, true},
		"missing root":  {ImageServer{Root: filepath.Join(root, "nope"), BaseURL: "https://x.example.com"}, true},
		"no base URL":   {ImageServer{Root: root}, true},
		"http base URL": {ImageServer{Root: root, BaseURL: "http://x.example.com"}, true},
		"no host":       {ImageServer{Root: root, BaseURL: "https://"}, true},
		"not a URL":     {ImageServer{Root: root, BaseURL: "://bad"}, true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.server.Validate()
			if tc.wantErr && err == nil {
				t.Error("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// A file root is a configuration mistake worth catching: serving from it would
// 404 every image with no explanation.
func TestImageServerValidateRejectsFileRoot(t *testing.T) {
	t.Parallel()

	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	s := ImageServer{Root: f, BaseURL: "https://x.example.com"}
	if err := s.Validate(); err == nil {
		t.Error("expected an error for a file used as the image root")
	}
}
