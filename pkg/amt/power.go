package amt

import (
	"context"
	"errors"
	"fmt"

	"github.com/device-management-toolkit/go-wsman-messages/v2/pkg/wsman/cim/power"
)

// PowerState is the observed power state of the managed host.
type PowerState string

const (
	// PowerOn corresponds to CIM PowerState 2.
	PowerOn PowerState = "On"
	// PowerOff corresponds to CIM PowerState 8.
	PowerOff PowerState = "Off"
	// PowerUnknown is any other value.
	PowerUnknown PowerState = "Unknown"
)

// ResetType names a power action using Redfish vocabulary, so the aggregator
// can pass values through without a second translation table.
type ResetType string

const (
	ResetOn               ResetType = "On"
	ResetForceOff         ResetType = "ForceOff"
	ResetGracefulShutdown ResetType = "GracefulShutdown"
	ResetForceRestart     ResetType = "ForceRestart"
	ResetGracefulRestart  ResetType = "GracefulRestart"
	ResetPowerCycle       ResetType = "PowerCycle"
	ResetNmi              ResetType = "Nmi"
)

// ErrUnsupportedResetType is returned for a reset type AMT cannot perform.
var ErrUnsupportedResetType = errors.New("amt: unsupported reset type")

// resetTypes maps Redfish reset vocabulary onto CIM power state changes.
//
// The graceful variants ask the OS to cooperate and are only honoured when the
// host is running something that listens; AMT reports success either way, so
// callers must verify rather than trust the return value.
var resetTypes = map[ResetType]power.PowerState{
	ResetOn:               power.PowerOn,                // 2
	ResetForceOff:         power.PowerOffHard,           // 8
	ResetGracefulShutdown: power.PowerOffSoftGraceful,   // 12
	ResetForceRestart:     power.MasterBusReset,         // 10
	ResetGracefulRestart:  power.MasterBusResetGraceful, // 14
	ResetPowerCycle:       power.PowerCycleOffHard,      // 5
	ResetNmi:              power.DiagnosticInterruptNMI, // 11
}

// SupportedResetTypes lists the reset types this package implements, in the
// order Redfish clients expect to see them.
func SupportedResetTypes() []ResetType {
	return []ResetType{
		ResetOn, ResetForceOff, ResetGracefulShutdown,
		ResetForceRestart, ResetGracefulRestart, ResetPowerCycle, ResetNmi,
	}
}

// PowerState reads the current power state of the managed host.
func (c *Client) PowerState(_ context.Context) (PowerState, error) {
	resp, err := c.msg.CIM.AssociatedPowerManagementService.Get()
	if err != nil {
		return PowerUnknown, fmt.Errorf("amt: reading power state: %w", err)
	}

	switch resp.Body.AssociatedPowerManagementService.PowerState {
	case 2:
		return PowerOn, nil
	case 8:
		return PowerOff, nil
	default:
		return PowerUnknown, nil
	}
}

// Reset performs a power action.
//
// A zero ReturnValue means AMT accepted the request, not that the host
// completed the transition. Callers that care should poll PowerState.
func (c *Client) Reset(_ context.Context, t ResetType) error {
	state, ok := resetTypes[t]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedResetType, t)
	}

	resp, err := c.msg.CIM.PowerManagementService.RequestPowerStateChange(state)
	if err != nil {
		return fmt.Errorf("amt: requesting power state %v: %w", state, err)
	}
	if rv := resp.Body.RequestPowerStateChangeResponse.ReturnValue; rv != 0 {
		return fmt.Errorf("amt: power state change %q rejected with return value %d", t, rv)
	}
	return nil
}
