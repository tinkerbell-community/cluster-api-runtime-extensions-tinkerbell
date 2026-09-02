// Package janitor implements tinkerbell-hardware-janitor (C4): a controller
// that restores released Tinkerbell Hardware to a clean, safe baseline after
// CAPT gives it up — clearing the secret-bearing spec.userData that tootles
// would otherwise keep serving, clearing stale OS metadata (the C2 handoff),
// and parking the netboot posture. See docs/tinkerbell-hardware-janitor.md.
package janitor

const (
	// Name is the component name: binary, chart, image, field owner, and
	// event recorder (repo naming standard).
	Name = "tinkerbell-hardware-janitor"

	// Foreign contract keys (CAPT's claim/release markers).
	//
	// OwnerNameLabel and OwnerNamespaceLabel are set at claim and removed at
	// release (cluster-api-provider-tinkerbell controller/machine/hardware.go:27, :31).
	OwnerNameLabel      = "v1alpha1.tinkerbell.org/ownerName"
	OwnerNamespaceLabel = "v1alpha1.tinkerbell.org/ownerNamespace"
	// ProvisionedAnnotation is stamped on workflow success and deleted at
	// release (hardware.go:34); terraform pre-sets it on the bootstrap node
	// precisely so CAPT never claims or re-images it.
	ProvisionedAnnotation = "v1alpha1.tinkerbell.org/provisioned"

	// ScrubbedAtAnnotation records the last scrub (RFC3339); C4-exclusive,
	// informational, survives re-claims.
	ScrubbedAtAnnotation = "janitor.tinkerbell.org/scrubbed-at"

	// FieldManager attributes the janitor's updates in managedFields.
	FieldManager = Name
)

// Event reasons emitted on Hardware.
const (
	EventScrubbed     = "HardwareScrubbed"
	EventHalfReleased = "HardwareHalfReleased"
)
