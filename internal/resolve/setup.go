package resolve

import (
	"context"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TalosConfigGVK identifies the bootstrap config, watched as unstructured.
var TalosConfigGVK = schema.GroupVersionKind{
	Group:   "bootstrap.cluster.x-k8s.io",
	Version: "v1beta1",
	Kind:    "TalosConfig",
}

// SetupWithManager wires the controller. Primary reconcile is TinkerbellMachine (the concrete
// talosVersion is reached through it, and it fires on claim). Watches on Hardware (keyed on CAPT's
// claim labels) and on the bootstrap TalosConfig (unstructured) retrigger on annotation changes,
// re-classification, terraform re-applies, and version bumps (§3.5).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, concurrency int) error {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(TinkerbellMachineGVK)

	hardware := &tinkv1.Hardware{}

	talosConfig := &unstructured.Unstructured{}
	talosConfig.SetGroupVersionKind(TalosConfigGVK)

	return ctrl.NewControllerManagedBy(mgr).
		Named(Name).
		For(primary).
		Watches(hardware, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			return hardwareToTinkerbellMachine(obj)
		})).
		Watches(talosConfig, handler.EnqueueRequestsFromMapFunc(r.talosConfigToTinkerbellMachine)).
		WithOptions(controller.Options{MaxConcurrentReconciles: concurrency}).
		Complete(r)
}

// hardwareToTinkerbellMachine maps a Hardware to the TinkerbellMachine named by its claim labels.
func hardwareToTinkerbellMachine(obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	name := labels[OwnerNameLabel]
	namespace := labels[OwnerNamespaceLabel]
	if name == "" || namespace == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}}}
}

// talosConfigToTinkerbellMachine maps a bootstrap TalosConfig to its Machine's TinkerbellMachine
// via the owner Machine's infrastructureRef.
func (r *Reconciler) talosConfigToTinkerbellMachine(ctx context.Context, obj client.Object) []reconcile.Request {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind != "Machine" {
			continue
		}
		machine := &clusterv1.Machine{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref.Name}, machine); err != nil {
			return nil
		}
		if machine.Spec.InfrastructureRef.Name == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{
			Namespace: machine.Namespace, Name: machine.Spec.InfrastructureRef.Name,
		}}}
	}
	return nil
}
