// Package redfish serves a DMTF Redfish aggregator over the fleet of Intel
// AMT devices.
//
// AMT speaks WS-Management, not Redfish, so this package is the compatibility
// layer: it renders AMT facts and CIM inventory as Redfish resources and
// translates Redfish actions into WS-Man calls.
//
// It is not on the provisioning control path. Rufio drives power and PXE
// through bmclib's IntelAMT provider directly; if the aggregator is down,
// provisioning continues.
package redfish

import "context"

// Capabilities are the boot methods a device's firmware reports.
//
// They gate what the aggregator advertises. A device whose firmware does not
// report UEFI HTTPS boot gets no VirtualMedia resource at all, rather than one
// that accepts an image and then silently boots normally.
type Capabilities struct {
	PXE       bool
	HardDrive bool
	CD        bool
	UEFIHTTPS bool
}

// NIC is one host network interface.
type NIC struct {
	MACAddress string
	Name       string
}

// Device is one AMT device as the aggregator sees it.
//
// Everything here comes from the AMTDevice status written by the enrollment
// reconciler, so rendering a resource costs no device calls. Only genuinely
// live values -- power state -- are read through Client.
type Device struct {
	// ID is the AMT platform GUID, stable across reboots, address changes
	// and renames. It keys every Redfish resource.
	ID string
	// Name is the Kubernetes resource name.
	Name string
	// Host is the device address.
	Host string

	Manufacturer    string
	Model           string
	SerialNumber    string
	BaseboardModel  string
	BaseboardSerial string
	BIOSVersion     string
	CPUCount        int
	CPUModel        string
	MemoryBytes     int64
	NICs            []NIC

	AMTVersion  string
	ControlMode string

	Capabilities Capabilities

	// Reachable reports whether the last reconcile authenticated. An
	// unreachable device is still served -- with a Critical health status --
	// so it is visible rather than absent.
	Reachable bool
	// Phase is the enrollment phase, surfaced on the AggregationSource.
	Phase string
}

// Client performs live operations against one device.
type Client interface {
	// PowerState returns "On", "Off" or "Unknown".
	PowerState(ctx context.Context) (string, error)
	// Reset performs a Redfish reset type.
	Reset(ctx context.Context, resetType string) error
	// SetBootOverride arms a one-shot boot override for a Redfish boot
	// target.
	SetBootOverride(ctx context.Context, target string) error
	// ClearBootOverride disarms any pending override.
	ClearBootOverride(ctx context.Context) error
	// InsertVirtualMedia arms a one-shot boot from an HTTPS image.
	InsertVirtualMedia(ctx context.Context, imageURL string, enforceSecureBoot bool) error
	// EjectVirtualMedia clears a pending media boot.
	EjectVirtualMedia(ctx context.Context) error
	// Close releases any resources held by the client.
	Close() error
}

// Store provides the devices the aggregator serves.
type Store interface {
	// List returns every known device.
	List(ctx context.Context) ([]Device, error)
	// Get returns one device by ID, or ErrNotFound.
	Get(ctx context.Context, id string) (Device, error)
	// Connect opens an authenticated session for live operations.
	Connect(ctx context.Context, id string) (Client, error)
}
