package redfish

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// ErrNotFound is returned by a Store for an unknown device.
var ErrNotFound = errors.New("redfish: device not found")

// Base is the Redfish service root path. It is fixed by the specification.
const Base = "/redfish/v1"

// OData property names that appear on nearly every resource.
const (
	odataType  = "@odata.type"
	odataIDKey = "@odata.id"
	keyName    = "Name"
	keyLinks   = "Links"
)

// Server serves the Redfish aggregator.
type Server struct {
	// Store provides the devices served.
	Store Store
	// ServiceUUID identifies this aggregator in the service root. It should
	// be stable across restarts so clients do not see the service change
	// identity.
	ServiceUUID string
	// EnforceSecureBoot requires virtual-media boot images to be signed. It
	// is fleet policy from AMTProfile rather than a per-request choice: a
	// caller able to relax secure boot per request could downgrade the
	// platform's boot integrity by asking nicely.
	EnforceSecureBoot bool
	// Log receives request and error logging.
	Log *slog.Logger
}

// Handler returns the HTTP handler for the aggregator.
//
// Routing uses the Go 1.22 pattern syntax rather than a router dependency:
// the Redfish surface is a fixed, small set of paths, and the method-and-path
// patterns express it directly.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}

	mux := http.NewServeMux()

	// The service root is served at both the canonical path and its
	// trailing-slash form; clients differ on which they request, and
	// answering only one looks like an unreachable service.
	mux.HandleFunc("GET "+Base, s.serviceRoot)
	mux.HandleFunc("GET "+Base+"/", s.serviceRoot)
	mux.HandleFunc("GET /redfish/", s.versions)
	mux.HandleFunc("GET /redfish", s.versions)

	mux.HandleFunc("GET "+Base+"/Systems", s.systemCollection)
	mux.HandleFunc("GET "+Base+"/Systems/{id}", s.system)
	mux.HandleFunc("POST "+Base+"/Systems/{id}/Actions/ComputerSystem.Reset", s.reset)
	mux.HandleFunc("PATCH "+Base+"/Systems/{id}", s.patchSystem)

	mux.HandleFunc("GET "+Base+"/Systems/{id}/VirtualMedia", s.virtualMediaCollection)
	mux.HandleFunc("GET "+Base+"/Systems/{id}/VirtualMedia/Cd", s.virtualMedia)
	mux.HandleFunc("POST "+Base+"/Systems/{id}/VirtualMedia/Cd/Actions/VirtualMedia.InsertMedia", s.insertMedia)
	mux.HandleFunc("POST "+Base+"/Systems/{id}/VirtualMedia/Cd/Actions/VirtualMedia.EjectMedia", s.ejectMedia)

	mux.HandleFunc("GET "+Base+"/Chassis", s.chassisCollection)
	mux.HandleFunc("GET "+Base+"/Chassis/{id}", s.chassis)

	mux.HandleFunc("GET "+Base+"/Managers", s.managerCollection)
	mux.HandleFunc("GET "+Base+"/Managers/{id}", s.manager)

	mux.HandleFunc("GET "+Base+"/AggregationService", s.aggregationService)
	mux.HandleFunc("GET "+Base+"/AggregationService/AggregationSources", s.aggregationSourceCollection)
	mux.HandleFunc("GET "+Base+"/AggregationService/AggregationSources/{id}", s.aggregationSource)

	mux.HandleFunc("/", s.notFound)

	return s.withCommonHeaders(mux)
}

// withCommonHeaders sets the headers every Redfish response carries.
func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("OData-Version", "4.0")
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// writeJSON renders a resource.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.Log.Error("writing response", "path", r.URL.Path, "err", err)
	}
}

// Redfish error responses carry a structured body clients parse for the
// message id; a bare status code is valid HTTP but unhelpful to a Redfish
// client.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, messageID, message string) {
	s.writeJSON(w, r, status, map[string]any{
		"error": map[string]any{
			"code":    messageID,
			"message": message,
			"@Message.ExtendedInfo": []any{map[string]any{
				odataType:   "#Message.v1_1_2.Message",
				"MessageId": messageID,
				"Message":   message,
				"Severity":  severityFor(status),
			}},
		},
	})
}

func severityFor(status int) string {
	if status >= http.StatusInternalServerError {
		return "Critical"
	}
	return "Warning"
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, http.StatusNotFound, "Base.1.0.ResourceMissingAtURI",
		fmt.Sprintf("No resource at %s", r.URL.Path))
}

// device resolves the {id} path value, writing the error response itself when
// the device is unknown.
func (s *Server) device(w http.ResponseWriter, r *http.Request) (Device, bool) {
	id := r.PathValue("id")
	dev, err := s.Store.Get(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "Base.1.0.ResourceNotFound",
			fmt.Sprintf("Device %q is not known to this service", id))
		return Device{}, false
	case err != nil:
		s.Log.Error("reading device", "id", id, "err", err)
		s.writeError(w, r, http.StatusInternalServerError, "Base.1.0.InternalError",
			"Failed to read device")
		return Device{}, false
	}
	return dev, true
}

// decodeBody reads a JSON request body, writing the error response itself on
// malformed input.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.MalformedJSON",
			"Request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

// odataID builds a resource path.
func odataID(parts ...string) string {
	return Base + "/" + strings.Join(parts, "/")
}
