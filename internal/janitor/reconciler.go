package janitor

import (
	"context"
	"strings"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Reconciler scrubs released Hardware. The manager cache is filtered
// server-side to owner-label-absent Hardware (see cmd main), so claimed
// objects never even reach the informer; the remaining conjuncts are
// evaluated here.
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder
	Opts     Options
}

// Reconcile classifies one Hardware and scrubs it when released.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	hw := &tinkv1.Hardware{}
	if err := r.Get(ctx, req.NamespacedName, hw); err != nil {
		// Deleted, or re-claimed out of the cache's label selector — either
		// way the object left the half-released state, so its gauge series
		// must go with it (the resync repopulates genuine anomalies).
		if apierrors.IsNotFound(err) {
			halfReleased.DeleteLabelValues(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	class := Classify(hw)
	if class != ClassHalfReleased {
		halfReleased.DeleteLabelValues(hw.Namespace, hw.Name)
	}
	switch class {
	case ClassHalfReleased:
		// Ambiguous state (manual partial release): surfaced, never
		// auto-repaired. The informer resync revisits it.
		halfReleased.WithLabelValues(hw.Namespace, hw.Name).Set(1)
		r.Recorder.Eventf(hw, nil, corev1.EventTypeWarning, EventHalfReleased, EventHalfReleased,
			"owner labels absent but %s still present alongside userData; remove the annotation to complete the release", ProvisionedAnnotation)
		return ctrl.Result{}, nil
	case ClassReleased:
		// Continue below.
	default:
		// Claimed (stale cache), NeverClaimed, or BootstrapReserved.
		return ctrl.Result{}, nil
	}

	scrubbed := hw.DeepCopy()
	cleared := Scrub(scrubbed, r.Opts)
	if len(cleared) == 0 {
		return ctrl.Result{}, nil
	}
	// A resourceVersion-guarded Update, never a merge patch and never SSA:
	// the write means "clear these fields GIVEN the released state I saw".
	// A concurrent re-claim (owner labels + new userData) bumps the
	// resourceVersion, this update fails with a Conflict, and the fresh
	// reconcile re-classifies the object as Claimed and no-ops — a blind
	// patch landing late would erase the new machine's config instead.
	// Re-classification, not re-application, is the correctness mechanism;
	// in-process conflict-retry loops are forbidden here.
	if err := r.Update(ctx, scrubbed, client.FieldOwner(FieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			updateConflicts.Inc()
			log.V(1).Info("scrub conflicted with a concurrent write; re-classifying", "hardware", hw.Name)
		}
		return ctrl.Result{}, err
	}

	r.Recorder.Eventf(hw, nil, corev1.EventTypeNormal, EventScrubbed, EventScrubbed,
		"released hardware scrubbed: %s", strings.Join(cleared, ", "))
	scrubbedTotal.Inc()
	log.Info("scrubbed released hardware", "hardware", hw.Name, "fields", cleared)
	return ctrl.Result{}, nil
}

// SetupWithManager wires the controller. The cache (configured in cmd main)
// already restricts the informer to owner-label-absent Hardware in the
// target namespace, so a release arrives as a plain Add event.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(Name).
		For(&tinkv1.Hardware{}).
		Complete(r)
}
