package resolve

import (
	"context"
	"fmt"
	"strings"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// MachineNameLabel is CAPT's ownership label on the Workflow object
// (capt controller/machine/tinkerbellmachine.go). The ValidatingWebhookConfiguration's
// objectSelector requires it to Exist, so tink auto-enrollment Workflows — which carry
// no CAPT labels — are excluded at the API server's matching layer and never reach the
// gate, regardless of failure policy.
const MachineNameLabel = "capt.tinkerbell.org/machine-name"

// WorkflowGate is the mandatory Workflow-CREATE admission gate (spec §3.7). Vanilla CAPT
// creates the Workflow at claim with no operating_system awareness, the resolver writes
// the block asynchronously after claim, and tink-controller renders the Workflow exactly
// once — so a CREATE that races ahead of the resolver renders
// IMG_URL = https://factory.talos.dev/image///metal-.raw.zst and bricks the machine
// permanently (§7.3 forbids deleting a Workflow to force a re-render). The gate holds the
// CREATE until the target Hardware's operating_system block carries the three fields the
// shipped Template substitutes. CAPT backoff-retries the create, so a deny is retriable,
// not fatal.
type WorkflowGate struct {
	// Reader must be uncached (manager GetAPIReader): admission must not depend on
	// informer sync or lag behind a just-written operating_system block.
	Reader client.Reader
}

var _ admission.Validator[*tinkv1.Workflow] = &WorkflowGate{}

// SetupWithManager registers the gate on the manager's webhook server at the
// builder-derived path /validate-tinkerbell-org-v1alpha1-workflow. The webhook server is
// a non-leader-election runnable, so every replica answers admission (HA under
// failurePolicy: Fail, spec §2.4).
func (g *WorkflowGate) SetupWithManager(mgr ctrl.Manager) error {
	if g.Reader == nil {
		g.Reader = mgr.GetAPIReader()
	}
	if err := ctrl.NewWebhookManagedBy(mgr, &tinkv1.Workflow{}).WithValidator(g).Complete(); err != nil {
		return fmt.Errorf("registering workflow gate: %w", err)
	}
	return nil
}

// ValidateCreate denies until the target Hardware's operating_system block is complete.
func (g *WorkflowGate) ValidateCreate(ctx context.Context, wf *tinkv1.Workflow) (admission.Warnings, error) {
	if wf.Spec.HardwareRef == "" {
		return nil, fmt.Errorf("workflow %s/%s has no spec.hardwareRef; cannot verify the Hardware operating_system block it would render", wf.Namespace, wf.Name)
	}

	hw := &tinkv1.Hardware{}
	if err := g.Reader.Get(ctx, client.ObjectKey{Namespace: wf.Namespace, Name: wf.Spec.HardwareRef}, hw); err != nil {
		return nil, fmt.Errorf("reading Hardware %q for workflow %s/%s: %w", wf.Spec.HardwareRef, wf.Namespace, wf.Name, err)
	}

	if missing := missingOSFields(hw); len(missing) > 0 {
		return nil, fmt.Errorf(
			"hardware %s/%s spec.metadata.instance.operating_system is missing %s: "+
				"a Workflow rendered now would build a broken IMG_URL and permanently brick the machine "+
				"(workflows render exactly once); talos-image-resolver writes the block after claim — the CREATE is retried",
			hw.Namespace, hw.Name, strings.Join(missing, ", "))
	}
	return nil, nil
}

// missingOSFields names the empty operating_system fields the shipped Template substitutes
// into IMG_URL (slug, version, os_slug — §3.3). distro/image_tag feed only tootles metadata
// and are deliberately not gated.
func missingOSFields(hw *tinkv1.Hardware) []string {
	var osBlock *tinkv1.MetadataInstanceOperatingSystem
	if hw.Spec.Metadata != nil && hw.Spec.Metadata.Instance != nil {
		osBlock = hw.Spec.Metadata.Instance.OperatingSystem
	}
	if osBlock == nil {
		return []string{osFieldSlug, osFieldVersion, osFieldOsSlug}
	}
	var missing []string
	for _, f := range []struct{ name, value string }{
		{osFieldSlug, osBlock.Slug}, {osFieldVersion, osBlock.Version}, {osFieldOsSlug, osBlock.OsSlug},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// ValidateUpdate admits: the gate holds only the render-once CREATE; later mutations
// (tink-controller status flow, allowPXE disarm) are not its business.
func (g *WorkflowGate) ValidateUpdate(_ context.Context, _, _ *tinkv1.Workflow) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete admits.
func (g *WorkflowGate) ValidateDelete(_ context.Context, _ *tinkv1.Workflow) (admission.Warnings, error) {
	return nil, nil
}
