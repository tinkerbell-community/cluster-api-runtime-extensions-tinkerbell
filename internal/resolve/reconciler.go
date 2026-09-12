package resolve

import (
	"context"

	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// TinkerbellMachineGVK identifies the infra machine, read as unstructured so the resolver
// imports no CAPT fork api (the teardown precedent).
var TinkerbellMachineGVK = schema.GroupVersionKind{
	Group:   "infrastructure.cluster.x-k8s.io",
	Version: "v1beta2",
	Kind:    "TinkerbellMachine",
}

// EventIdentityResolved is emitted on Hardware when the image identity is applied.
const EventIdentityResolved = "ImageIdentityResolved"

// Plan is the decision the resolver reached for one Hardware: which fields to apply. A nil
// field means "do not write it".
type Plan struct {
	// OperatingSystem is the identity block; nil under provisioned-freeze or when no version resolved.
	OperatingSystem *tinkv1.MetadataInstanceOperatingSystem
	// InstallerImage is the resolver-owned installer-image annotation value.
	InstallerImage string
	// ContractPin is a newly established talos.tinkerbell.org/contract minor to stamp.
	ContractPin string
}

func (p *Plan) empty() bool {
	return p == nil || (p.OperatingSystem == nil && p.InstallerImage == "" && p.ContractPin == "")
}

// Reconciler resolves the per-machine schematic + Talos version and writes the image identity
// onto claimed Hardware by sparse server-side apply (field manager talos-image-resolver). It
// coexists with CAPT, which still resolves into its own status: same engine and inputs, different
// write target (§3.5).
type Reconciler struct {
	client.Client
	Recorder      events.EventRecorder
	Registrar     *Registrar
	Policy        VersionPolicy
	Customization CustomizationConfig
	// FactoryURL is the resolved factory host used to compose the installer reference.
	FactoryURL string
}

// Reconcile resolves one TinkerbellMachine's claimed Hardware.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tm := &unstructured.Unstructured{}
	tm.SetGroupVersionKind(TinkerbellMachineGVK)
	if err := r.Get(ctx, req.NamespacedName, tm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	hardwareName, _, _ := unstructured.NestedString(tm.Object, "spec", "hardwareName")
	if hardwareName == "" {
		return ctrl.Result{}, nil // not yet claimed to a Hardware
	}
	namespace, _, _ := unstructured.NestedString(tm.Object, "status", "targetNamespace")
	if namespace == "" {
		namespace = tm.GetNamespace()
	}

	hw := &tinkv1.Hardware{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: hardwareName}, hw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !isClaimed(hw, tm) {
		// Released or claimed by another machine: never our write (§3.5). Stop.
		return ctrl.Result{}, nil
	}

	machine, err := r.ownerMachine(ctx, tm)
	if err != nil {
		return ctrl.Result{}, err
	}
	raw := SpecTalosVersion(ctx, r.Client, machine)

	plan, err := r.resolve(ctx, tm, hw, raw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if plan.empty() {
		versionsUnresolved.Inc()
		return ctrl.Result{}, nil
	}

	if err := r.apply(ctx, hw, plan); err != nil {
		if apierrors.IsConflict(err) {
			applyConflicts.Inc()
			log.V(1).Info("apply conflicted with a concurrent write; requeueing", "hardware", hw.Name)
		}
		return ctrl.Result{}, err
	}
	if plan.OperatingSystem != nil {
		identitiesResolved.Inc()
		r.Recorder.Eventf(hw, nil, corev1.EventTypeNormal, EventIdentityResolved, EventIdentityResolved,
			"resolved image identity: slug=%s version=%s", plan.OperatingSystem.Slug, plan.OperatingSystem.Version)
		log.Info("resolved image identity", "hardware", hw.Name, "slug", plan.OperatingSystem.Slug, "version", plan.OperatingSystem.Version)
	}
	return ctrl.Result{}, nil
}

// isClaimed verifies CAPT's claim: the Hardware's owner labels name this TinkerbellMachine.
// Load-bearing — the resolver never writes released or unclaimed Hardware (§3.5).
func isClaimed(hw *tinkv1.Hardware, tm *unstructured.Unstructured) bool {
	return hw.Labels[OwnerNameLabel] == tm.GetName() &&
		hw.Labels[OwnerNamespaceLabel] == tm.GetNamespace()
}

// resolve computes the Plan for a claimed Hardware. Provisioned Hardware freezes its
// operating_system (leaves the provisioning-time value) but still refreshes the installer-image
// annotation so the upgrade path stays current; an unresolvable version writes nothing (§3.4/§3.5).
func (r *Reconciler) resolve(ctx context.Context, tm *unstructured.Unstructured, hw *tinkv1.Hardware, rawVersion string) (*Plan, error) {
	provisioned := IsProvisioned(hw)
	version, newPin, err := r.Policy.Resolve(ctx, rawVersion, provisioned, hw.GetAnnotations()[ContractAnnotation])
	if err != nil {
		return nil, err
	}
	if version == "" {
		if newPin == "" {
			return nil, nil
		}
		return &Plan{ContractPin: newPin}, nil
	}

	signals := SignalsFromHardware(hw, MachineExtensions(tm.GetAnnotations()), r.Customization)
	id, err := r.Registrar.Register(ctx, Build(signals))
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		InstallerImage: InstallerImage(r.FactoryURL, id, version),
		ContractPin:    newPin,
	}

	// Keep the installer image the install action recorded: re-asserting the same value
	// keeps the resolver a co-owner of the annotation under server-side apply without
	// changing it, so the upgrade path names the schematic that is actually on the disk.
	if annotations := hw.GetAnnotations(); annotations[UserDataOwnerAnnotation] == UserDataOwnerTalos2disk {
		if installed := annotations[InstallerImageAnnotation]; installed != "" {
			plan.InstallerImage = installed
		}
	}
	if !provisioned {
		plan.OperatingSystem = &tinkv1.MetadataInstanceOperatingSystem{
			Slug:     id,
			Distro:   distro,
			Version:  version,
			ImageTag: version,
			OsSlug:   OSSlug(version, signals.Architecture),
		}
	}
	return plan, nil
}

// ownerMachine returns the owning core Machine, or nil when none is set / found.
func (r *Reconciler) ownerMachine(ctx context.Context, tm *unstructured.Unstructured) (*clusterv1.Machine, error) {
	for _, ref := range tm.GetOwnerReferences() {
		if ref.Kind != "Machine" {
			continue
		}
		machine := &clusterv1.Machine{}
		err := r.Get(ctx, client.ObjectKey{Namespace: tm.GetNamespace(), Name: ref.Name}, machine)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return machine, nil
	}
	return nil, nil
}

// apply writes the Plan onto the Hardware as a sparse server-side apply carrying only identity,
// the operating_system path, and the resolver's granular annotation keys — never userData, disks,
// instance.id/hostname, or C1's annotations (§3.5). The read resourceVersion is the optimistic
// precondition; ForceOwnership makes the coexistence handoff from talos-os-metadata deterministic.
func (r *Reconciler) apply(ctx context.Context, hw *tinkv1.Hardware, plan *Plan) error {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tinkerbell.org/v1alpha1",
		"kind":       "Hardware",
		"metadata": map[string]any{
			"name":            hw.Name,
			"namespace":       hw.Namespace,
			"resourceVersion": hw.ResourceVersion,
		},
	}}

	annotations := map[string]any{}
	if plan.InstallerImage != "" {
		annotations[InstallerImageAnnotation] = plan.InstallerImage
	}
	if plan.ContractPin != "" {
		annotations[ContractAnnotation] = plan.ContractPin
	}
	if len(annotations) > 0 {
		if err := unstructured.SetNestedMap(u.Object, annotations, "metadata", "annotations"); err != nil {
			return err
		}
	}
	if plan.OperatingSystem != nil {
		os := plan.OperatingSystem
		if err := unstructured.SetNestedMap(u.Object, map[string]any{
			osFieldSlug:    os.Slug,
			"distro":       os.Distro,
			osFieldVersion: os.Version,
			"image_tag":    os.ImageTag,
			osFieldOsSlug:  os.OsSlug,
		}, "spec", "metadata", "instance", "operating_system"); err != nil {
			return err
		}
	}

	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(FieldManager), client.ForceOwnership)
}
