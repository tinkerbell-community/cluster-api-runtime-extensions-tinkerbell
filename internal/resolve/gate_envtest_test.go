//go:build envtest

package resolve

// End-to-end admission semantics for the mandatory Workflow-CREATE gate: the deny while
// operating_system is incomplete, the admit once the resolver has written it, and — just as
// load-bearing — the objectSelector exclusion that keeps tink auto-enrollment Workflows
// (unlabeled) outside the gate entirely. The selector match happens at the API server, so
// only a real API server can prove it. Run: make test-envtest

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// gateWebhookPath is the builder-derived registration path for tinkv1.Workflow.
const gateWebhookPath = "/validate-tinkerbell-org-v1alpha1-workflow"

// gateWebhookConfiguration mirrors the chart's ValidatingWebhookConfiguration exactly:
// workflows CREATE only, failurePolicy Fail, and the objectSelector that requires CAPT's
// machine-name label to exist. envtest rewrites clientConfig to point at the local server.
func gateWebhookConfiguration() *admissionv1.ValidatingWebhookConfiguration {
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	scope := admissionv1.NamespacedScope
	return &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "talos-image-resolver-workflow-gate"},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name: "workflow-gate.resolve.tinkerbell.org",
			ClientConfig: admissionv1.WebhookClientConfig{
				Service: &admissionv1.ServiceReference{
					Name: "talos-image-resolver", Namespace: "default", Path: ptrTo(gateWebhookPath),
				},
			},
			Rules: []admissionv1.RuleWithOperations{{
				Operations: []admissionv1.OperationType{admissionv1.Create},
				Rule: admissionv1.Rule{
					APIGroups:   []string{"tinkerbell.org"},
					APIVersions: []string{"v1alpha1"},
					Resources:   []string{"workflows"},
					Scope:       &scope,
				},
			}},
			ObjectSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: MachineNameLabel, Operator: metav1.LabelSelectorOpExists,
				}},
			},
			FailurePolicy:           &fail,
			SideEffects:             &none,
			AdmissionReviewVersions: []string{"v1"},
			TimeoutSeconds:          ptrTo(int32(10)),
		}},
	}
}

func ptrTo[T any](v T) *T { return &v }

// startGateServer serves the WorkflowGate on envtest's local webhook endpoint and blocks
// until the TLS listener answers, so a test never races the server start.
func startGateServer(t *testing.T) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(tinkv1.AddToScheme(scheme))

	srv := webhook.NewServer(webhook.Options{
		Host:    envWebhook.LocalServingHost,
		Port:    envWebhook.LocalServingPort,
		CertDir: envWebhook.LocalServingCertDir,
	})
	srv.Register(gateWebhookPath, admission.WithValidator(scheme, &WorkflowGate{Reader: envClient}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		if err := srv.Start(ctx); err != nil {
			t.Errorf("webhook server exited: %v", err)
		}
	}()

	addr := fmt.Sprintf("%s:%d", envWebhook.LocalServingHost, envWebhook.LocalServingPort)
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", addr,
			&tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- local readiness probe only
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook server never became ready on %s: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func gateTestWorkflow(ns, name, hardwareRef string, labeled bool) *tinkv1.Workflow {
	wf := &tinkv1.Workflow{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       tinkv1.WorkflowSpec{TemplateRef: "talos-install", HardwareRef: hardwareRef},
	}
	if labeled {
		wf.Labels = map[string]string{MachineNameLabel: "m1"}
	}
	return wf
}

func TestEnvtestWorkflowGate(t *testing.T) {
	startGateServer(t)
	ctx := context.Background()
	ns := newNamespace(t)

	unresolved := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{Name: "hw-empty", Namespace: ns}}
	if err := envClient.Create(ctx, unresolved); err != nil {
		t.Fatal(err)
	}

	// A CAPT-labeled Workflow against unresolved Hardware is denied — the race that
	// would render IMG_URL = .../image///metal-.raw.zst is stopped at admission.
	denied := gateTestWorkflow(ns, "wf-denied", "hw-empty", true)
	err := envClient.Create(ctx, denied)
	if err == nil {
		t.Fatal("labeled Workflow against unresolved Hardware was admitted; the gate is not holding")
	}
	if !apierrors.IsForbidden(err) {
		t.Fatalf("deny error = %v, want Forbidden admission denial", err)
	}
	if !strings.Contains(err.Error(), "operating_system") {
		t.Errorf("deny message %q does not name operating_system", err.Error())
	}

	// An unlabeled (auto-enrollment) Workflow against the same unresolved Hardware is
	// admitted: the objectSelector excludes it at the API server's matching layer.
	autoEnroll := gateTestWorkflow(ns, "wf-auto", "hw-empty", false)
	if err := envClient.Create(ctx, autoEnroll); err != nil {
		t.Fatalf("unlabeled Workflow must bypass the gate, got: %v", err)
	}

	// Once the resolver has written the block, the same labeled CREATE is admitted.
	resolved := &tinkv1.Hardware{ObjectMeta: metav1.ObjectMeta{
		Name: "hw-resolved", Namespace: ns,
		Labels: map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: ns},
	}}
	if err := envClient.Create(ctx, resolved); err != nil {
		t.Fatal(err)
	}
	live := &tinkv1.Hardware{}
	if err := envClient.Get(ctx, client.ObjectKeyFromObject(resolved), live); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: envClient, FactoryURL: "http://factory.example.test"}
	if err := r.apply(ctx, live, plan("sid123", "v1.13.9", "factory.example.test/metal-installer/sid123:v1.13.9")); err != nil {
		t.Fatal(err)
	}
	admitted := gateTestWorkflow(ns, "wf-admitted", "hw-resolved", true)
	if err := envClient.Create(ctx, admitted); err != nil {
		t.Fatalf("labeled Workflow against resolved Hardware must be admitted, got: %v", err)
	}
}
