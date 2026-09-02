package janitor

import (
	"fmt"
	"time"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/utils/ptr"
)

// Options parameterize the scrub.
type Options struct {
	// ClearOSMetadata clears metadata.instance.operating_system and
	// metadata.instance.state (the C2 handoff). The escape hatch exists for
	// environments still on terraform-written OS metadata (P1 unmet, C2
	// undeployed), where clearing would break the next Workflow render.
	ClearOSMetadata bool
	// BaselineAllowPXE is the parked netboot posture asserted on interfaces
	// that already carry a netboot block. false matches the stack's own
	// end-of-workflow posture and the bootstrap node's.
	BaselineAllowPXE bool
	// Now is the clock for the scrubbed-at stamp (test seam).
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Scrub mutates hw in place — only the target fields, never rebuilding the
// spec, so discovery-owned fields are structurally untouchable — and returns
// the names of the fields it actually changed (empty means nothing to do).
// Clearing userData falsifies the predicate's U conjunct, so a scrubbed
// object permanently leaves the released set (self-extinguishing); replayed
// events hit the nothing-changed short-circuit.
func Scrub(hw *tinkv1.Hardware, opts Options) []string {
	var changed []string
	if hw.Spec.UserData != nil {
		hw.Spec.UserData = nil
		changed = append(changed, "spec.userData")
	}
	if opts.ClearOSMetadata && hw.Spec.Metadata != nil && hw.Spec.Metadata.Instance != nil {
		instance := hw.Spec.Metadata.Instance
		if instance.OperatingSystem != nil {
			instance.OperatingSystem = nil
			changed = append(changed, "metadata.instance.operating_system")
		}
		if instance.State != "" {
			instance.State = ""
			changed = append(changed, "metadata.instance.state")
		}
	}
	for i := range hw.Spec.Interfaces {
		netboot := hw.Spec.Interfaces[i].Netboot
		if netboot == nil {
			continue
		}
		if netboot.AllowPXE == nil || *netboot.AllowPXE != opts.BaselineAllowPXE {
			netboot.AllowPXE = ptr.To(opts.BaselineAllowPXE)
			changed = append(changed, fmt.Sprintf("interfaces[%d].netboot.allowPXE parked", i))
		}
	}
	if len(changed) > 0 {
		annotations := hw.Annotations
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[ScrubbedAtAnnotation] = opts.now().UTC().Format(time.RFC3339)
		hw.Annotations = annotations
	}
	return changed
}
