package resolve

import (
	"context"
	"strings"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func gateScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(tinkv1.AddToScheme(scheme))
	return scheme
}

func gateHardware(name string, os *tinkv1.MetadataInstanceOperatingSystem) *tinkv1.Hardware {
	hw := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tink"}}
	if os != nil {
		hw.Spec.Metadata = &tinkv1.HardwareMetadata{Instance: &tinkv1.MetadataInstance{OperatingSystem: os}}
	}
	return hw
}

func gateWorkflow(hardwareRef string) *tinkv1.Workflow {
	return &tinkv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: "m1", Namespace: "tink"},
		Spec:       tinkv1.WorkflowSpec{TemplateRef: "talos-install-abc", HardwareRef: hardwareRef},
	}
}

// TestWorkflowGateValidateCreate covers the deny/admit table for the mandatory
// Workflow-CREATE gate: a render against an incomplete operating_system block
// yields IMG_URL = .../image///metal-.raw.zst and a permanent brick (spec §3.7).
func TestWorkflowGateValidateCreate(t *testing.T) {
	completeOS := &tinkv1.MetadataInstanceOperatingSystem{
		Slug: "sid123", Distro: "talos", Version: "v1.13.9", ImageTag: "v1.13.9", OsSlug: "talos-v1.13.9-amd64",
	}

	tests := []struct {
		name     string
		hardware *tinkv1.Hardware
		workflow *tinkv1.Workflow
		wantDeny string // empty means admit
	}{
		{
			name:     "complete block admits",
			hardware: gateHardware("hw-1", completeOS),
			workflow: gateWorkflow("hw-1"),
		},
		{
			name:     "no operating_system block denies",
			hardware: gateHardware("hw-1", nil),
			workflow: gateWorkflow("hw-1"),
			wantDeny: "operating_system",
		},
		{
			name:     "empty slug denies",
			hardware: gateHardware("hw-1", &tinkv1.MetadataInstanceOperatingSystem{Version: "v1.13.9", OsSlug: "talos-v1.13.9-amd64"}),
			workflow: gateWorkflow("hw-1"),
			wantDeny: "slug",
		},
		{
			name:     "empty version denies",
			hardware: gateHardware("hw-1", &tinkv1.MetadataInstanceOperatingSystem{Slug: "sid123", OsSlug: "talos-v1.13.9-amd64"}),
			workflow: gateWorkflow("hw-1"),
			wantDeny: "version",
		},
		{
			name:     "empty os_slug denies",
			hardware: gateHardware("hw-1", &tinkv1.MetadataInstanceOperatingSystem{Slug: "sid123", Version: "v1.13.9"}),
			workflow: gateWorkflow("hw-1"),
			wantDeny: "os_slug",
		},
		{
			name:     "hardware not found denies",
			hardware: gateHardware("other", completeOS),
			workflow: gateWorkflow("hw-1"),
			wantDeny: "not found",
		},
		{
			name:     "missing hardwareRef denies",
			hardware: gateHardware("hw-1", completeOS),
			workflow: gateWorkflow(""),
			wantDeny: "hardwareRef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(gateScheme(t)).WithObjects(tt.hardware).Build()
			g := &WorkflowGate{Reader: c}

			_, err := g.ValidateCreate(context.Background(), tt.workflow)
			if tt.wantDeny == "" {
				if err != nil {
					t.Fatalf("ValidateCreate() = %v, want admit", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCreate() admitted, want deny mentioning %q", tt.wantDeny)
			}
			if !strings.Contains(err.Error(), tt.wantDeny) {
				t.Errorf("deny message %q does not mention %q", err.Error(), tt.wantDeny)
			}
		})
	}
}

// TestWorkflowGateUpdateDeleteAdmit asserts UPDATE/DELETE pass through: the gate holds only
// the render-once CREATE; later mutations are tink-controller's own business.
func TestWorkflowGateUpdateDeleteAdmit(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(gateScheme(t)).Build()
	g := &WorkflowGate{Reader: c}
	wf := gateWorkflow("hw-1")
	if _, err := g.ValidateUpdate(context.Background(), wf, wf); err != nil {
		t.Fatalf("ValidateUpdate() = %v, want admit", err)
	}
	if _, err := g.ValidateDelete(context.Background(), wf); err != nil {
		t.Fatalf("ValidateDelete() = %v, want admit", err)
	}
}
