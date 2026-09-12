// Package resolve resolves a per-machine Talos Image Factory schematic and Talos
// version and writes the resulting image identity onto claimed Tinkerbell Hardware.
//
// It ports cluster-api-provider-tinkerbell's pkg/schematic engine and version policy
// unchanged, extends the schematic Customization with the bootable-image inputs the
// Factory honors for the raw disk (§3.2), and, unlike CAPT, writes the identity onto
// Hardware.spec.metadata.instance.operating_system by sparse server-side apply rather
// than into a provider status. See docs/runtime-extensions-migration.md §3.
package resolve

import tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"

const (
	// Name is the component name: binary, chart, image, field owner, event recorder.
	Name = "talos-image-resolver"

	// FieldManager attributes the resolver's server-side applies in managedFields.
	// A field manager is a string, not a process: it stays distinct even though this
	// logic is folded into one manager binary (spec §2.6).
	FieldManager = Name

	// DefaultFactoryURL is the public Image Factory.
	DefaultFactoryURL = "https://factory.talos.dev"

	// ExtensionsAnnotation lists official system extensions to include (comma separated).
	// C1 is the exclusive writer of this key on Hardware; TinkerbellMachine may also carry it.
	ExtensionsAnnotation = "talos.tinkerbell.org/system-extensions"

	// ContractAnnotation pins the Talos minor a machine tracks, e.g. "v1.14". The resolver
	// stamps it on first resolution of an unpinned, unprovisioned machine and owns it.
	ContractAnnotation = "talos.tinkerbell.org/contract"

	// OverlayAnnotation carries the Image Factory overlay (C1-written). Value is "name" or
	// "name@image", e.g. "rpi_generic@siderolabs/sbc-raspberrypi".
	OverlayAnnotation = "talos.tinkerbell.org/overlay"

	// ExtraKernelArgsAnnotation carries additional kernel args (C1-written, comma separated).
	ExtraKernelArgsAnnotation = "talos.tinkerbell.org/extra-kernel-args"

	// InstallerImageAnnotation publishes the upgrade-rendezvous installer reference; resolver-owned.
	InstallerImageAnnotation = "talos.tinkerbell.org/installer-image"

	// OwnerNameLabel / OwnerNamespaceLabel are CAPT's claim markers, set to the
	// TinkerbellMachine's name/namespace at claim and removed at release.
	OwnerNameLabel      = "v1alpha1.tinkerbell.org/ownerName"
	OwnerNamespaceLabel = "v1alpha1.tinkerbell.org/ownerNamespace"

	// ProvisionedAnnotation is stamped by CAPT on workflow success and drives the
	// provisioned-freeze split (§3.5).
	ProvisionedAnnotation = "v1alpha1.tinkerbell.org/provisioned"

	// distro is the constant operating_system.distro value.
	distro = "talos"

	// operating_system leaf keys, shared by the sparse apply and the Workflow gate.
	osFieldSlug    = "slug"
	osFieldVersion = "version"
	osFieldOsSlug  = "os_slug"

	// nvmeCLIExtension is the NVMe userspace tooling built in when an NVMe disk is present.
	nvmeCLIExtension = "siderolabs/nvme-cli"

	archAMD64 = "amd64"
	archARM64 = "arm64"
)

// IsProvisioned reports whether CAPT has finished provisioning the Hardware.
func IsProvisioned(hw *tinkv1.Hardware) bool {
	_, ok := hw.GetAnnotations()[ProvisionedAnnotation]
	return ok
}
