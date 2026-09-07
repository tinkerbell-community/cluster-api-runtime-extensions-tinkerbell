package redfish

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ImageServer serves boot images for UEFI HTTPS boot.
//
// It exists because AMT fetches the image itself rather than having it
// streamed to it: the device performs an ordinary HTTPS GET, so the image must
// live somewhere it can reach, behind a certificate it trusts. Co-locating the
// server with the controller keeps that certificate and the trusted root
// pushed to devices in one place, where they cannot drift apart.
type ImageServer struct {
	// Root is the directory images are served from.
	Root string
	// BaseURL is the externally reachable https:// base URL of this server.
	// It is what gets handed to AMT, so it must be resolvable and verifiable
	// from the device, not from the controller.
	BaseURL string
	// Log receives request logging.
	Log *slog.Logger
}

// ImagePrefix is the path images are served under.
const ImagePrefix = "/images/"

// Validate checks the server is usable before anything depends on it.
//
// A misconfigured image server is worth failing fast on: the alternative is a
// boot that appears to succeed at every step and then silently does not
// happen, which is the hardest failure in this whole system to diagnose.
func (s *ImageServer) Validate() error {
	if s.Root == "" {
		return errors.New("redfish: image root is required")
	}
	info, err := os.Stat(s.Root)
	if err != nil {
		return fmt.Errorf("redfish: image root %q: %w", s.Root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("redfish: image root %q is not a directory", s.Root)
	}

	if s.BaseURL == "" {
		return errors.New("redfish: image base URL is required")
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil {
		return fmt.Errorf("redfish: image base URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("redfish: image base URL must be https, got %q: AMT performs HTTPS boot only", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("redfish: image base URL must include a host")
	}
	return nil
}

// URLFor returns the URL a device should fetch an image from.
func (s *ImageServer) URLFor(name string) (string, error) {
	clean := path.Base(filepath.ToSlash(name))
	if clean == "" || clean == "." || clean == "/" {
		return "", fmt.Errorf("redfish: invalid image name %q", name)
	}
	base := strings.TrimSuffix(s.BaseURL, "/")
	return base + ImagePrefix + url.PathEscape(clean), nil
}

// Handler serves the image directory.
//
// http.FileServer is deliberately not used: it renders directory listings and
// follows nested paths, and this server should expose exactly the flat set of
// images placed in the root, nothing else about the filesystem.
//
// Opens go through os.Root, which confines them to the image directory in the
// kernel rather than relying on this code having reasoned correctly about
// every path encoding.
func (s *ImageServer) Handler() http.Handler {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, ImagePrefix)
		// Only a bare filename is accepted. Anything with a separator is
		// rejected outright rather than cleaned, so no traversal can be
		// constructed out of encodings this code did not anticipate.
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			http.NotFound(w, r)
			return
		}

		dir, err := os.OpenRoot(s.Root)
		if err != nil {
			log.Error("opening image root", "root", s.Root, "err", err)
			http.Error(w, "image store unavailable", http.StatusInternalServerError)
			return
		}
		defer dir.Close()

		f, err := dir.Open(name)
		if err != nil {
			log.Warn("image not served", "name", name, "err", err)
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		log.Info("serving boot image", "name", name, "bytes", info.Size(),
			"client", r.RemoteAddr)
		w.Header().Set("Content-Type", "application/octet-stream")
		// ServeContent handles range requests, which matter here: firmware
		// HTTPS boot commonly fetches an image in ranges rather than as one
		// stream.
		http.ServeContent(w, r, name, info.ModTime(), f)
	})
}
