package amtenroll

import (
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

// applyFacts copies observed device facts into status.
func applyFacts(status *amtv1.AMTDeviceStatus, conn *verified) {
	f := conn.facts

	status.TLSFingerprint = conn.fingerprint
	status.ProvisioningState = f.ProvisioningState
	status.ControlMode = f.ControlMode
	status.AllowedControlModes = f.AllowedControlModes
	status.DigestRealm = f.DigestRealm
	status.Firmware = amtv1.FirmwareInfo{
		AMT:      f.Firmware.AMT,
		Build:    f.Firmware.Build,
		SKU:      f.Firmware.SKU,
		Recovery: f.Firmware.Recovery,
	}
	status.BootCapabilities = amtv1.BootCapabilities{
		PXE:               f.BootCapabilities.PXE,
		HardDrive:         f.BootCapabilities.HardDrive,
		CDDVD:             f.BootCapabilities.CDDVD,
		UEFIHTTPS:         f.BootCapabilities.UEFIHTTPS,
		IDER:              f.BootCapabilities.IDER,
		SOL:               f.BootCapabilities.SOL,
		BIOSSetup:         f.BootCapabilities.BIOSSetup,
		SecureBootControl: f.BootCapabilities.SecureBootControl,
	}
	status.Redirection = amtv1.RedirectionStatus{
		EnabledState:    int32(f.Redirection.EnabledState), //nolint:gosec // AMT reports a small enum
		ListenerEnabled: f.Redirection.ListenerEnabled,
	}
}

// applyInventory copies the collected inventory summary into status.
func applyInventory(status *amtv1.AMTDeviceStatus, inv *amt.Inventory) {
	s := inv.Summary
	summary := &amtv1.InventorySummary{
		Manufacturer:    s.Manufacturer,
		Model:           s.Model,
		SerialNumber:    s.SerialNumber,
		BaseboardModel:  s.BaseboardModel,
		BaseboardSerial: s.BaseboardSerial,
		BIOSVersion:     s.BIOSVersion,
		CPUCount:        int32(s.CPUCount), //nolint:gosec // socket counts are small
		CPUModel:        s.CPUModel,
		MemoryBytes:     s.MemoryBytes,
	}
	for _, n := range s.NICs {
		summary.NICs = append(summary.NICs, amtv1.NIC{
			MACAddress: n.MACAddress,
			Name:       n.Name,
		})
	}
	status.Inventory = summary
}

// setCondition records a condition, preserving LastTransitionTime when the
// status has not changed so a steady-state resync does not churn the object.
func setCondition(status *amtv1.AMTDeviceStatus, condType string, state metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range status.Conditions {
		c := &status.Conditions[i]
		if c.Type != condType {
			continue
		}
		if c.Status != state {
			c.LastTransitionTime = now
		}
		c.Status = state
		c.Reason = reason
		c.Message = message
		return
	}
	status.Conditions = append(status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             state,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// equalStatus reports whether two statuses are equivalent for the purpose of
// deciding whether to write.
//
// Condition timestamps are ignored: setCondition only advances
// LastTransitionTime on an actual change, but the resync would otherwise
// still write on every pass because the conditions slice is rebuilt.
func equalStatus(a, b *amtv1.AMTDeviceStatus) bool {
	x := a.DeepCopy()
	y := b.DeepCopy()
	normalizeConditions(x)
	normalizeConditions(y)
	// LastVerifiedTime advances on every successful reconcile by design; it
	// is not a reason to write on its own.
	x.LastVerifiedTime = nil
	y.LastVerifiedTime = nil
	return equality.Semantic.DeepEqual(x, y)
}

func normalizeConditions(status *amtv1.AMTDeviceStatus) {
	for i := range status.Conditions {
		status.Conditions[i].LastTransitionTime = metav1.Time{}
		status.Conditions[i].ObservedGeneration = 0
	}
}
