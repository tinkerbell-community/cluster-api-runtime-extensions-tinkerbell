package redfish

import (
	"net/http"
	"net/url"
	"strings"
)

// virtualMediaGuard rejects requests for devices whose firmware does not
// report UEFI HTTPS boot.
//
// The alternative -- accepting the image and letting the device boot normally
// -- is the worst possible failure: every call returns success and the only
// symptom is a machine that quietly did not do what was asked.
func (s *Server) virtualMediaGuard(w http.ResponseWriter, r *http.Request) (Device, bool) {
	dev, ok := s.device(w, r)
	if !ok {
		return Device{}, false
	}
	if !dev.Capabilities.UEFIHTTPS {
		s.writeError(w, r, http.StatusNotFound, "Base.1.0.ResourceNotFound",
			"This device's firmware does not report UEFI HTTPS boot, so it has no virtual media")
		return Device{}, false
	}
	return dev, true
}

func (s *Server) virtualMediaCollection(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.virtualMediaGuard(w, r)
	if !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:  "#VirtualMediaCollection.VirtualMediaCollection",
		odataIDKey: odataID("Systems", dev.ID, "VirtualMedia"),
		keyName:    "Virtual Media Collection",
		"Members": []any{
			link(odataID("Systems", dev.ID, "VirtualMedia", "Cd")),
		},
		"Members@odata.count": 1,
	})
}

func (s *Server) virtualMedia(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.virtualMediaGuard(w, r)
	if !ok {
		return
	}
	base := odataID("Systems", dev.ID, "VirtualMedia", "Cd")
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:              "#VirtualMedia.v1_6_4.VirtualMedia",
		odataIDKey:             base,
		"Id":                   "Cd",
		keyName:                "Virtual CD",
		"MediaTypes":           []string{"CD", "DVD"},
		"ConnectedVia":         "URI",
		"TransferProtocolType": "HTTPS",
		// Inserted is not tracked: AMT clears the boot parameters once
		// consumed and offers no way to read back a pending image, so
		// reporting true would be a guess that goes stale at the next boot.
		"Inserted":       false,
		"Image":          nil,
		"WriteProtected": true,
		"Actions": map[string]any{
			"#VirtualMedia.InsertMedia": map[string]any{
				"target": base + "/Actions/VirtualMedia.InsertMedia",
				"TransferProtocolType@Redfish.AllowableValues": []string{"HTTPS"},
			},
			"#VirtualMedia.EjectMedia": map[string]any{
				"target": base + "/Actions/VirtualMedia.EjectMedia",
			},
		},
	})
}

func (s *Server) insertMedia(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.virtualMediaGuard(w, r)
	if !ok {
		return
	}

	var req struct {
		Image                string `json:"Image"`
		TransferProtocolType string `json:"TransferProtocolType"`
		Inserted             *bool  `json:"Inserted"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}

	if err := validateImage(req.Image, req.TransferProtocolType); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.ActionParameterValueError", err.Error())
		return
	}

	client, err := s.Store.Connect(r.Context(), dev.ID)
	if err != nil {
		s.connectError(w, r, dev, err)
		return
	}
	defer client.Close()

	if err := client.InsertVirtualMedia(r.Context(), req.Image, s.EnforceSecureBoot); err != nil {
		s.actionError(w, r, "inserting virtual media", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ejectMedia(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.virtualMediaGuard(w, r)
	if !ok {
		return
	}

	client, err := s.Store.Connect(r.Context(), dev.ID)
	if err != nil {
		s.connectError(w, r, dev, err)
		return
	}
	defer client.Close()

	if err := client.EjectVirtualMedia(r.Context()); err != nil {
		s.actionError(w, r, "ejecting virtual media", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateImage rejects images AMT cannot fetch, before any device state is
// touched.
//
// The scheme check is the important one: AMT performs HTTPS boot only, and an
// http:// URL is accepted by every WS-Man call in the sequence before the
// device silently boots normally.
func validateImage(image, protocol string) error {
	if image == "" {
		return errImage("Image is required")
	}
	if protocol != "" && !strings.EqualFold(protocol, "HTTPS") {
		return errImage("TransferProtocolType must be HTTPS; AMT performs HTTPS boot only")
	}
	u, err := url.Parse(image)
	if err != nil {
		return errImage("Image is not a valid URL: " + err.Error())
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return errImage("Image must be an https:// URL; AMT performs HTTPS boot only")
	}
	if u.Host == "" {
		return errImage("Image must include a host")
	}
	return nil
}

type imageError string

func (e imageError) Error() string { return string(e) }

func errImage(msg string) error { return imageError(msg) }
