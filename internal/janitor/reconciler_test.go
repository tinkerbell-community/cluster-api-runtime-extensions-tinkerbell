package janitor

import (
	"context"
	"strings"
	"testing"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func janitorScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := tinkv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client, *events.FakeRecorder) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(janitorScheme(t)).WithObjects(objs...).Build()
	recorder := events.NewFakeRecorder(16)
	return &Reconciler{Client: c, Recorder: recorder, Opts: testOptions()}, c, recorder
}

func reconcileHW(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "tinkerbell", Name: name},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func getHW(t *testing.T, c client.Client, name string) *tinkv1.Hardware {
	t.Helper()
	hw := &tinkv1.Hardware{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "tinkerbell", Name: name}, hw); err != nil {
		t.Fatal(err)
	}
	return hw
}

func TestReconcileScrubsReleased(t *testing.T) {
	r, c, recorder := newReconciler(t, releasedHardware())

	reconcileHW(t, r, "hw-1")

	hw := getHW(t, c, "hw-1")
	if hw.Spec.UserData != nil {
		t.Error("released hardware's userData not scrubbed")
	}
	if hw.Spec.Metadata.Instance.OperatingSystem != nil {
		t.Error("operating_system not scrubbed")
	}
	select {
	case e := <-recorder.Events:
		if !strings.Contains(e, EventScrubbed) {
			t.Errorf("event = %q, want %s", e, EventScrubbed)
		}
	default:
		t.Error("no HardwareScrubbed event emitted")
	}

	// Self-extinguishing: a second reconcile performs no write.
	before := hw.ResourceVersion
	reconcileHW(t, r, "hw-1")
	if after := getHW(t, c, "hw-1").ResourceVersion; after != before {
		t.Errorf("second reconcile wrote (rv %s -> %s)", before, after)
	}
}

func TestReconcileIgnoresClaimedNeverClaimedBootstrap(t *testing.T) {
	claimed := releasedHardware()
	claimed.Name = "claimed"
	claimed.Labels = map[string]string{OwnerNameLabel: "m1", OwnerNamespaceLabel: "tinkerbell"}

	neverClaimed := hardwareWith(false, "", false)
	neverClaimed.Name = "fresh"

	bootstrap := hardwareWith(false, "", true)
	bootstrap.Name = "bootstrap"

	r, c, recorder := newReconciler(t, claimed, neverClaimed, bootstrap)

	for _, name := range []string{"claimed", "fresh", "bootstrap"} {
		before := getHW(t, c, name).ResourceVersion
		reconcileHW(t, r, name)
		if after := getHW(t, c, name).ResourceVersion; after != before {
			t.Errorf("%s: reconcile wrote (rv %s -> %s); must be untouched", name, before, after)
		}
	}
	if hw := getHW(t, c, "claimed"); hw.Spec.UserData == nil {
		t.Error("claimed hardware's userData was cleared")
	}
	select {
	case e := <-recorder.Events:
		t.Errorf("unexpected event: %q", e)
	default:
	}
}

func TestReconcileHalfReleasedSurfacedNotScrubbed(t *testing.T) {
	half := hardwareWith(false, "#cloud-config", true)
	half.Name = "half"
	r, c, recorder := newReconciler(t, half)

	before := getHW(t, c, "half").ResourceVersion
	reconcileHW(t, r, "half")

	hw := getHW(t, c, "half")
	if hw.ResourceVersion != before {
		t.Error("half-released hardware was written; must only be surfaced")
	}
	if hw.Spec.UserData == nil {
		t.Error("half-released userData was cleared")
	}
	select {
	case e := <-recorder.Events:
		if !strings.Contains(e, EventHalfReleased) {
			t.Errorf("event = %q, want %s", e, EventHalfReleased)
		}
	default:
		t.Error("no HardwareHalfReleased event emitted")
	}
}
