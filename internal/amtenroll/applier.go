package amtenroll

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// applier writes the resources describing one AMT device.
//
// It follows the same server-side-apply discipline as the discovery
// controller (docs/discovery-field-ownership.md): every write is a sparse
// apply asserting only fields this component owns, creates are plain POSTs so
// a race surfaces as AlreadyExists rather than silent adoption, and resources
// without this component's managed-by label are never modified.
type applier struct {
	client client.Client
	now    func() time.Time
	log    *slog.Logger
}

// ownership carries the identifying values stamped onto every managed
// resource.
type ownership struct {
	platformGUID string
	deviceName   string
}

// apply gets the live object into live (same concrete type as desired) and
// either creates it or server-side-applies the sparse desired object.
//
// carry, when non-nil, runs against the live object before the apply so the
// caller can serialize foreign state it owns but does not author -- the
// Hardware interfaces list is SSA-atomic, so netboot values written by the
// tink workflow controller must be carried forward inside it.
func (a *applier) apply(
	ctx context.Context,
	kind string,
	live, desired client.Object,
	own ownership,
	carry func(live client.Object),
) error {
	a.stamp(desired, own)
	log := a.log.With("kind", kind, "name", desired.GetName(), "namespace", desired.GetNamespace())

	err := a.client.Get(ctx, client.ObjectKeyFromObject(desired), live)
	switch {
	case apierrors.IsNotFound(err):
		if err := a.client.Create(ctx, desired, client.FieldOwner(FieldManager)); err != nil {
			return err
		}
		log.Info("created resource")
		return nil
	case err != nil:
		return err
	}

	if live.GetLabels()[ManagedByLabel] != ManagedByValue {
		// Hand-provisioned resources, and resources belonging to the mDNS
		// discovery controller, are never adopted. The guard is evaluated
		// against the same revision the apply is conditioned on, so a label
		// stripped between the check and the write surfaces as a Conflict
		// rather than a corruption.
		log.Warn("skipping resource not managed by this component")
		return nil
	}
	if carry != nil {
		carry(live)
	}

	desired.SetResourceVersion(live.GetResourceVersion())
	cfg, err := applyConfigurationFor(desired)
	if err != nil {
		return err
	}
	// ForceOwnership is scoped to the fields present in the sparse apply
	// configuration, every one of which belongs to this component by
	// contract. Foreign fields are structurally unreachable: they are never
	// serialized.
	if err := a.client.Apply(ctx, client.ApplyConfigurationFromUnstructured(cfg),
		client.FieldOwner(FieldManager), client.ForceOwnership); err != nil {
		return err
	}
	log.Info("applied resource")
	return nil
}

// stamp sets the owned label and annotations on the sparse desired object.
// Maps are SSA-granular, so only these keys are asserted.
func (a *applier) stamp(obj client.Object, own ownership) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[ManagedByLabel] = ManagedByValue
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[LastSeenAnnotation] = a.now().UTC().Format(time.RFC3339)
	if own.platformGUID != "" {
		annotations[PlatformGUIDAnnotation] = own.platformGUID
	}
	if own.deviceName != "" {
		annotations[DeviceAnnotation] = own.deviceName
	}
	obj.SetAnnotations(annotations)
}

// applyConfigurationFor converts the sparse desired object into its SSA
// payload, dropping the zero-value noise typed objects carry: status is a
// subresource and never this component's to assert, and an empty
// creationTimestamp would be applied as a field.
func applyConfigurationFor(desired client.Object) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return nil, fmt.Errorf("converting %T to apply configuration: %w", desired, err)
	}
	u := &unstructured.Unstructured{Object: content}
	unstructured.RemoveNestedField(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	return u, nil
}
