package redfish

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
)

// resetTypes are the Redfish reset types this service supports. The list is
// what pkg/amt can actually map onto CIM power state changes; advertising
// more would invite requests that always fail.
var resetTypes = []string{
	"On", "ForceOff", "GracefulShutdown", "ForceRestart", "GracefulRestart",
	"PowerCycle", "Nmi",
}

// bootTargets are the Redfish boot targets this service supports.
func bootTargetsFor(c Capabilities) []string {
	targets := []string{"None"}
	if c.PXE {
		targets = append(targets, "Pxe")
	}
	if c.HardDrive {
		targets = append(targets, "Hdd")
	}
	if c.CD {
		targets = append(targets, "Cd")
	}
	if c.UEFIHTTPS {
		targets = append(targets, "UefiHttp")
	}
	targets = append(targets, "BiosSetup")
	return targets
}

func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"v1": Base + "/",
	})
}

func (s *Server) serviceRoot(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		odataType:            "#ServiceRoot.v1_15_0.ServiceRoot",
		odataIDKey:           Base,
		"Id":                 "RootService",
		keyName:              "Intel AMT Redfish Aggregator",
		"RedfishVersion":     "1.15.0",
		"Systems":            link(odataID("Systems")),
		"Chassis":            link(odataID("Chassis")),
		"Managers":           link(odataID("Managers")),
		"AggregationService": link(odataID("AggregationService")),
		keyLinks:             map[string]any{},
	}
	// An empty UUID is not a valid Redfish service identity, so the property
	// is omitted rather than emitted blank when none is configured.
	if s.ServiceUUID != "" {
		body["UUID"] = s.ServiceUUID
	}
	s.writeJSON(w, r, http.StatusOK, body)
}

// collection renders a Redfish collection. Members are links only, which is
// what the specification requires and also what makes listing the fleet free:
// no device is contacted to render it.
func (s *Server) collection(w http.ResponseWriter, r *http.Request, odataType, id, name, path string, ids []string) {
	sort.Strings(ids)
	members := make([]any, 0, len(ids))
	for _, deviceID := range ids {
		members = append(members, link(odataID(path, deviceID)))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:             odataType,
		odataIDKey:            odataID(path),
		"Id":                  id,
		keyName:               name,
		"Members":             members,
		"Members@odata.count": len(members),
	})
}

func (s *Server) deviceIDs(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	devices, err := s.Store.List(r.Context())
	if err != nil {
		s.Log.Error("listing devices", "err", err)
		s.writeError(w, r, http.StatusInternalServerError, "Base.1.0.InternalError",
			"Failed to list devices")
		return nil, false
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	return ids, true
}

func (s *Server) systemCollection(w http.ResponseWriter, r *http.Request) {
	ids, ok := s.deviceIDs(w, r)
	if !ok {
		return
	}
	s.collection(w, r, "#ComputerSystemCollection.ComputerSystemCollection",
		"ComputerSystemCollection", "Computer System Collection", "Systems", ids)
}

func (s *Server) chassisCollection(w http.ResponseWriter, r *http.Request) {
	ids, ok := s.deviceIDs(w, r)
	if !ok {
		return
	}
	s.collection(w, r, "#ChassisCollection.ChassisCollection",
		"ChassisCollection", "Chassis Collection", "Chassis", ids)
}

func (s *Server) managerCollection(w http.ResponseWriter, r *http.Request) {
	ids, ok := s.deviceIDs(w, r)
	if !ok {
		return
	}
	s.collection(w, r, "#ManagerCollection.ManagerCollection",
		"ManagerCollection", "Manager Collection", "Managers", ids)
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}

	// Power state is the one genuinely live value: everything else changes
	// at reboot cadence and is served from the reconciler's status. The
	// collection above is links only, so this costs one call per system
	// actually requested rather than one per system in the fleet.
	powerState := "Unknown"
	if dev.Reachable {
		if client, err := s.Store.Connect(r.Context(), dev.ID); err != nil {
			s.Log.Warn("connecting for power state", "id", dev.ID, "err", err)
		} else {
			defer client.Close()
			if state, err := client.PowerState(r.Context()); err != nil {
				s.Log.Warn("reading power state", "id", dev.ID, "err", err)
			} else {
				powerState = state
			}
		}
	}

	body := map[string]any{
		odataType:      "#ComputerSystem.v1_22_0.ComputerSystem",
		odataIDKey:     odataID("Systems", dev.ID),
		"Id":           dev.ID,
		keyName:        dev.Name,
		"SystemType":   "Physical",
		"Manufacturer": dev.Manufacturer,
		"Model":        dev.Model,
		"SerialNumber": dev.SerialNumber,
		"UUID":         dev.ID,
		"PowerState":   powerState,
		"Status":       statusFor(dev),
		"BiosVersion":  dev.BIOSVersion,
		"Boot": map[string]any{
			"BootSourceOverrideEnabled": "Once",
			"BootSourceOverrideTarget":  "None",
			// Only one-shot overrides are offered. AMT clears the boot
			// parameters once consumed, so Continuous cannot be honoured and
			// advertising it would be a lie.
			"BootSourceOverrideEnabled@Redfish.AllowableValues": []string{"Disabled", "Once"},
			"BootSourceOverrideTarget@Redfish.AllowableValues":  bootTargetsFor(dev.Capabilities),
		},
		"ProcessorSummary": map[string]any{
			"Count": dev.CPUCount,
			"Model": dev.CPUModel,
		},
		"MemorySummary": map[string]any{
			"TotalSystemMemoryGiB": bytesToGiB(dev.MemoryBytes),
		},
		"Actions": map[string]any{
			"#ComputerSystem.Reset": map[string]any{
				"target":                            odataID("Systems", dev.ID, "Actions", "ComputerSystem.Reset"),
				"ResetType@Redfish.AllowableValues": resetTypes,
			},
		},
		keyLinks: map[string]any{
			"Chassis":   []any{link(odataID("Chassis", dev.ID))},
			"ManagedBy": []any{link(odataID("Managers", dev.ID))},
		},
	}

	// VirtualMedia is advertised only when the firmware reports UEFI HTTPS
	// boot. A device without it gets no link rather than one that accepts an
	// image and then boots normally.
	if dev.Capabilities.UEFIHTTPS {
		body["VirtualMedia"] = link(odataID("Systems", dev.ID, "VirtualMedia"))
	}

	s.writeJSON(w, r, http.StatusOK, body)
}

// patchSystem handles boot override changes.
func (s *Server) patchSystem(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}

	var req struct {
		Boot *struct {
			BootSourceOverrideEnabled string `json:"BootSourceOverrideEnabled"`
			BootSourceOverrideTarget  string `json:"BootSourceOverrideTarget"`
		} `json:"Boot"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	if req.Boot == nil {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.PropertyMissing",
			"Only the Boot property can be patched on this service")
		return
	}

	// Continuous is rejected rather than silently downgraded to Once: a
	// caller that asked for a persistent override and got a single-use one
	// would be surprised at the second boot, not the first.
	if enabled := req.Boot.BootSourceOverrideEnabled; enabled == "Continuous" {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.PropertyValueNotInList",
			"BootSourceOverrideEnabled=Continuous is not supported: AMT clears boot parameters once consumed")
		return
	}

	client, err := s.Store.Connect(r.Context(), dev.ID)
	if err != nil {
		s.connectError(w, r, dev, err)
		return
	}
	defer client.Close()

	target := req.Boot.BootSourceOverrideTarget
	if target == "" || target == "None" || req.Boot.BootSourceOverrideEnabled == "Disabled" {
		if err := client.ClearBootOverride(r.Context()); err != nil {
			s.actionError(w, r, "clearing boot override", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if !supported(bootTargetsFor(dev.Capabilities), target) {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.PropertyValueNotInList",
			fmt.Sprintf("BootSourceOverrideTarget %q is not supported by this device", target))
		return
	}
	if err := client.SetBootOverride(r.Context(), target); err != nil {
		s.actionError(w, r, "setting boot override", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}

	var req struct {
		ResetType string `json:"ResetType"`
	}
	if !s.decodeBody(w, r, &req) {
		return
	}
	if !supported(resetTypes, req.ResetType) {
		s.writeError(w, r, http.StatusBadRequest, "Base.1.0.ActionParameterNotSupported",
			fmt.Sprintf("ResetType %q is not supported", req.ResetType))
		return
	}

	client, err := s.Store.Connect(r.Context(), dev.ID)
	if err != nil {
		s.connectError(w, r, dev, err)
		return
	}
	defer client.Close()

	if err := client.Reset(r.Context(), req.ResetType); err != nil {
		s.actionError(w, r, "performing reset", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) chassis(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:      "#Chassis.v1_25_0.Chassis",
		odataIDKey:     odataID("Chassis", dev.ID),
		"Id":           dev.ID,
		keyName:        dev.Name,
		"ChassisType":  "RackMount",
		"Manufacturer": dev.Manufacturer,
		"Model":        dev.Model,
		"SerialNumber": dev.SerialNumber,
		"Status":       statusFor(dev),
		keyLinks: map[string]any{
			"ComputerSystems": []any{link(odataID("Systems", dev.ID))},
			"ManagedBy":       []any{link(odataID("Managers", dev.ID))},
		},
	})
}

func (s *Server) manager(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		odataType:         "#Manager.v1_19_0.Manager",
		odataIDKey:        odataID("Managers", dev.ID),
		"Id":              dev.ID,
		keyName:           "Intel AMT",
		"ManagerType":     "BMC",
		"Manufacturer":    "Intel Corporation",
		"Model":           "Intel AMT",
		"FirmwareVersion": dev.AMTVersion,
		"Status":          statusFor(dev),
		"Oem": map[string]any{
			"IntelAMT": map[string]any{
				"ControlMode": dev.ControlMode,
				"Host":        dev.Host,
			},
		},
		keyLinks: map[string]any{
			"ManagerForServers": []any{link(odataID("Systems", dev.ID))},
			"ManagerForChassis": []any{link(odataID("Chassis", dev.ID))},
		},
	})
}

// connectError distinguishes a device that cannot be reached from a service
// fault: the former is the device's problem and must not read as a 500.
func (s *Server) connectError(w http.ResponseWriter, r *http.Request, dev Device, err error) {
	s.Log.Warn("connecting to device", "id", dev.ID, "err", err)
	s.writeError(w, r, http.StatusServiceUnavailable, "Base.1.0.ResourceAtUriUnauthorized",
		fmt.Sprintf("Cannot reach device %s: %v", dev.ID, err))
}

func (s *Server) actionError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.Log.Error(what, "err", err)
	status := http.StatusInternalServerError
	code := "Base.1.0.InternalError"
	if errors.Is(err, ErrUnsupported) {
		status = http.StatusBadRequest
		code = "Base.1.0.ActionParameterNotSupported"
	}
	s.writeError(w, r, status, code, fmt.Sprintf("Failed %s: %v", what, err))
}

// ErrUnsupported marks an operation the device cannot perform, as distinct
// from one that failed.
var ErrUnsupported = errors.New("redfish: operation not supported by device")

func statusFor(dev Device) map[string]any {
	state, health := "Enabled", "OK"
	if !dev.Reachable {
		state, health = "UnavailableOffline", "Critical"
	}
	return map[string]any{"State": state, "Health": health}
}

func link(id string) map[string]any { return map[string]any{odataIDKey: id} }

func supported(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// bytesToGiB renders memory the way Redfish expects, rounded to the nearest
// whole GiB.
func bytesToGiB(b int64) int64 {
	const giB = 1 << 30
	if b <= 0 {
		return 0
	}
	return (b + giB/2) / giB
}
