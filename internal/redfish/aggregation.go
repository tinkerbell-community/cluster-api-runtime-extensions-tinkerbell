package redfish

import "net/http"

// The AggregationService is the spec-faithful way to say "this service speaks
// for machines that are not itself". Each AggregationSource names one AMT
// device and carries its enrollment state, so a device that is unreachable or
// not yet provisioned is visible through Redfish rather than silently absent
// from the Systems collection.

func (s *Server) aggregationService(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:            "#AggregationService.v1_0_2.AggregationService",
		odataIDKey:           odataID("AggregationService"),
		"Id":                 "AggregationService",
		keyName:              "Aggregation Service",
		"ServiceEnabled":     true,
		"AggregationSources": link(odataID("AggregationService", "AggregationSources")),
	})
}

func (s *Server) aggregationSourceCollection(w http.ResponseWriter, r *http.Request) {
	devices, err := s.Store.List(r.Context())
	if err != nil {
		s.Log.Error("listing devices", "err", err)
		s.writeError(w, r, http.StatusInternalServerError, "Base.1.0.InternalError",
			"Failed to list devices")
		return
	}
	members := make([]any, 0, len(devices))
	for _, d := range devices {
		members = append(members, link(odataID("AggregationService", "AggregationSources", d.ID)))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:             "#AggregationSourceCollection.AggregationSourceCollection",
		odataIDKey:            odataID("AggregationService", "AggregationSources"),
		keyName:               "Aggregation Source Collection",
		"Members":             members,
		"Members@odata.count": len(members),
	})
}

func (s *Server) aggregationSource(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:  "#AggregationSource.v1_4_0.AggregationSource",
		odataIDKey: odataID("AggregationService", "AggregationSources", dev.ID),
		"Id":       dev.ID,
		keyName:    dev.Name,
		// AMT is not one of Redfish's named connection methods, so the
		// source is described as OEM rather than mislabelled as Redfish.
		"HostName": dev.Host,
		"Status":   statusFor(dev),
		"Oem": map[string]any{
			"IntelAMT": map[string]any{
				"EnrollmentPhase": dev.Phase,
				"ControlMode":     dev.ControlMode,
				"FirmwareVersion": dev.AMTVersion,
				"Protocol":        "WSMAN",
			},
		},
		keyLinks: map[string]any{
			"ConnectionMethod": nil,
			"ResourcesAccessed": []any{
				link(odataID("Systems", dev.ID)),
				link(odataID("Chassis", dev.ID)),
				link(odataID("Managers", dev.ID)),
			},
		},
	})
}
