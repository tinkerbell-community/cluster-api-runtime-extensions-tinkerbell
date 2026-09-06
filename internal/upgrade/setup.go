package upgrade

import (
	"context"
	"fmt"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SetupWithManager wires the coordinator: primary watch on TalosControlPlane
// (generation or upgrade.tinkerbell.org annotation changes — a terraform version
// bump fires here), secondary watch on Machine UpToDate/version transitions —
// what wakes the coordinator when an in-place rollout finishes. Workload Nodes
// cannot be watched from the management cluster, so blocked gates poll via
// RequeueAfter instead.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(TalosControlPlaneGVK)

	err := ctrl.NewControllerManagedBy(mgr).
		Named(Name).
		For(tcp, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			upgradeAnnotationsChanged(),
		))).
		Watches(&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.machineToTCP),
			builder.WithPredicates(machineConvergenceChanged())).
		Complete(r)
	if err != nil {
		return fmt.Errorf("building %s controller: %w", Name, err)
	}
	_ = concurrency // single TCP per cluster; serialized reconciles are the correctness posture
	return nil
}

// upgradeAnnotationsChanged fires on any change to upgrade.tinkerbell.org/*
// annotations (force-sync being set is the interesting one).
func upgradeAnnotationsChanged() predicate.Funcs {
	filter := func(annotations map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range annotations {
			if strings.HasPrefix(k, "upgrade.tinkerbell.org/") {
				out[k] = v
			}
		}
		return out
	}
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return true
			}
			oldA, newA := filter(e.ObjectOld.GetAnnotations()), filter(e.ObjectNew.GetAnnotations())
			if len(oldA) != len(newA) {
				return true
			}
			for k, v := range newA {
				if oldA[k] != v {
					return true
				}
			}
			return false
		},
	}
}

// machineConvergenceChanged fires when a Machine's UpToDate condition or version
// changes — the signals that move the gate.
func machineConvergenceChanged() predicate.Funcs {
	upToDate := func(obj client.Object) string {
		m, ok := obj.(*clusterv1.Machine)
		if !ok {
			return ""
		}
		cond := apimeta.FindStatusCondition(m.Status.Conditions, clusterv1.MachineUpToDateCondition)
		if cond == nil {
			return "absent"
		}
		return string(cond.Status)
	}
	version := func(obj client.Object) string {
		m, ok := obj.(*clusterv1.Machine)
		if !ok {
			return ""
		}
		return m.Spec.Version
	}
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return upToDate(e.ObjectOld) != upToDate(e.ObjectNew) || version(e.ObjectOld) != version(e.ObjectNew)
		},
	}
}

// machineToTCP maps a Machine to its cluster's TalosControlPlane via the
// cluster-name label and the Cluster's controlPlaneRef.
func (r *Reconciler) machineToTCP(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName := obj.GetLabels()[clusterv1.ClusterNameLabel]
	if clusterName == "" {
		return nil
	}
	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: clusterName}, cluster); err != nil {
		return nil
	}
	ref := cluster.Spec.ControlPlaneRef
	if ref.Kind != TalosControlPlaneGVK.Kind || ref.Name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{
		Namespace: obj.GetNamespace(), Name: ref.Name,
	}}}
}
