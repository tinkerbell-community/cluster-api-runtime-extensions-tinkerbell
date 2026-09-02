package janitor

import (
	"strings"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
)

// Class is the outcome of the released-hardware predicate's truth table
// (docs/tinkerbell-hardware-janitor.md): three booleans — owner labels
// absent (O), spec.userData non-empty (U), provisioned annotation absent
// (P) — and the janitor scrubs iff O AND U AND P.
type Class int

const (
	// ClassClaimed: owner labels present — CAPT's hardware, never touched.
	ClassClaimed Class = iota
	// ClassNeverClaimed: no owner, no userData, no provisioned annotation —
	// fresh inventory (or an already-scrubbed object), nothing to scrub.
	ClassNeverClaimed
	// ClassBootstrapReserved: no owner, no userData, provisioned annotation
	// present — the terraform bootstrap node's shape. It fails two
	// independent conjuncts (U and P), so a bug in either still leaves the
	// machine that anchors the management cluster protected.
	ClassBootstrapReserved
	// ClassHalfReleased: no owner, userData present, provisioned annotation
	// present — a manual partial release. Ambiguous; surfaced, never
	// auto-repaired.
	ClassHalfReleased
	// ClassReleased: no owner, userData present, no provisioned annotation —
	// CAPT's releaseHardware ran; scrub.
	ClassReleased
)

// String names the class for events, logs, and metrics.
func (c Class) String() string {
	switch c {
	case ClassClaimed:
		return "Claimed"
	case ClassNeverClaimed:
		return "NeverClaimed"
	case ClassBootstrapReserved:
		return "BootstrapReserved"
	case ClassHalfReleased:
		return "HalfReleased"
	case ClassReleased:
		return "Released"
	}
	return "Unknown"
}

// Classify evaluates the truth table for one Hardware object.
func Classify(hw *tinkv1.Hardware) Class {
	_, hasOwnerName := hw.Labels[OwnerNameLabel]
	_, hasOwnerNamespace := hw.Labels[OwnerNamespaceLabel]
	if hasOwnerName || hasOwnerNamespace {
		return ClassClaimed
	}
	hasUserData := hw.Spec.UserData != nil && strings.TrimSpace(*hw.Spec.UserData) != ""
	_, provisioned := hw.Annotations[ProvisionedAnnotation]
	switch {
	case !hasUserData && !provisioned:
		return ClassNeverClaimed
	case !hasUserData && provisioned:
		return ClassBootstrapReserved
	case provisioned:
		return ClassHalfReleased
	default:
		return ClassReleased
	}
}
