package amtenroll

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	bmcv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/bmc"
	tinkv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/tinkerbell"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/sync"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

// DefaultResyncInterval is how often a healthy device is re-read.
const DefaultResyncInterval = time.Hour

// DefaultRetryInterval is how soon an unreachable or unverifiable device is
// retried.
const DefaultRetryInterval = 5 * time.Minute

// Reconciler reconciles AMTDevice resources.
type Reconciler struct {
	Client client.Client

	// DefaultProfile is the AMTProfile used when a device does not name one.
	DefaultProfile string
	// Timeout bounds each device operation.
	Timeout time.Duration
	// ResyncInterval is how often a healthy device is re-read.
	ResyncInterval time.Duration
	// RetryInterval is how soon a failing device is retried.
	RetryInterval time.Duration
	// FacilityCode fills metadata.facility.facility_code on Hardware.
	FacilityCode string
	// AutoEnrollment enables Tinkerbell auto enrollment on Hardware.
	AutoEnrollment bool

	Now func() time.Time
	Log *slog.Logger
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.applyDefaults()
	return ctrl.NewControllerManagedBy(mgr).
		For(&amtv1.AMTDevice{}).
		Owns(&corev1.Secret{}).
		Named("amtenroll").
		Complete(r)
}

func (r *Reconciler) applyDefaults() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.Timeout <= 0 {
		r.Timeout = amt.DefaultTimeout
	}
	if r.ResyncInterval <= 0 {
		r.ResyncInterval = DefaultResyncInterval
	}
	if r.RetryInterval <= 0 {
		r.RetryInterval = DefaultRetryInterval
	}
	if r.DefaultProfile == "" {
		r.DefaultProfile = "default"
	}
}

// Reconcile brings one AMTDevice up to date.
//
// The device is the source of truth for everything in status: nothing is
// inferred or remembered across reconciles, so a device that is
// reconfigured out of band converges on the next pass rather than keeping a
// stale picture.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.applyDefaults()

	device := &amtv1.AMTDevice{}
	if err := r.Client.Get(ctx, req.NamespacedName, device); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !device.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	log := r.Log.With("device", device.Name, "namespace", device.Namespace,
		"host", device.Spec.Endpoint.Host)

	status := device.Status.DeepCopy()
	status.ObservedGeneration = device.Generation

	result, err := r.reconcileDevice(ctx, device, status, log)
	if updateErr := r.patchStatus(ctx, device, status); updateErr != nil {
		if err == nil {
			return ctrl.Result{}, updateErr
		}
		log.Error("updating status", "err", updateErr)
	}
	return result, err
}

func (r *Reconciler) reconcileDevice(
	ctx context.Context,
	device *amtv1.AMTDevice,
	status *amtv1.AMTDeviceStatus,
	log *slog.Logger,
) (ctrl.Result, error) {
	profile, err := r.profileFor(ctx, device)
	if err != nil {
		setCondition(status, amtv1.ConditionReachable, metav1.ConditionUnknown,
			"ProfileUnavailable", err.Error())
		status.Phase = amtv1.PhaseFailed
		return ctrl.Result{RequeueAfter: r.RetryInterval}, err
	}

	candidates, err := r.candidates(ctx, device, profile)
	if err != nil {
		status.Phase = amtv1.PhaseFailed
		setCondition(status, amtv1.ConditionCredentialsVerified, metav1.ConditionFalse,
			"NoCandidates", err.Error())
		return ctrl.Result{RequeueAfter: r.RetryInterval}, nil
	}

	conn, err := r.connect(ctx, device, candidates)
	if err != nil {
		status.Phase = amtv1.PhaseFailed
		setCondition(status, amtv1.ConditionReachable, metav1.ConditionFalse,
			"Unreachable", err.Error())
		setCondition(status, amtv1.ConditionCredentialsVerified, metav1.ConditionFalse,
			"NotVerified", "no candidate credentials authenticated")
		log.Info("device unreachable or unverifiable; will retry", "err", err)
		return ctrl.Result{RequeueAfter: r.RetryInterval}, nil
	}

	applyFacts(status, conn)
	setCondition(status, amtv1.ConditionReachable, metav1.ConditionTrue, "Reachable",
		"AMT WS-Man endpoint answered")
	setCondition(status, amtv1.ConditionCredentialsVerified, metav1.ConditionTrue, "Verified",
		fmt.Sprintf("authenticated as %q", conn.credentials.Username))
	status.LastVerifiedTime = &metav1.Time{Time: r.Now()}
	status.Phase = amtv1.PhaseVerified

	// An unprovisioned device answers enough to be identified but has no
	// admin credential and cannot be managed. It is reported rather than
	// skipped so a newly racked machine is visible instead of absent.
	if !conn.facts.Provisioned() {
		status.Phase = amtv1.PhaseUnprovisioned
		setCondition(status, amtv1.ConditionProvisioned, metav1.ConditionFalse,
			"PreProvisioning",
			"device is not provisioned; onboard it out of band before it can be managed")
		log.Info("device is not provisioned", "provisioningState", conn.facts.ProvisioningState)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	setCondition(status, amtv1.ConditionProvisioned, metav1.ConditionTrue,
		"Provisioned", "device is in "+conn.facts.ProvisioningState)

	inv, err := conn.client.Inventory(ctx)
	if err != nil {
		setCondition(status, amtv1.ConditionInventoryCollected, metav1.ConditionFalse,
			"InventoryFailed", err.Error())
		return ctrl.Result{RequeueAfter: r.RetryInterval}, fmt.Errorf("collecting inventory: %w", err)
	}
	applyInventory(status, inv)
	setCondition(status, amtv1.ConditionInventoryCollected, metav1.ConditionTrue,
		"Collected", "hardware inventory read from CIM")

	if err := r.register(ctx, device, status, conn, inv, log); err != nil {
		setCondition(status, amtv1.ConditionRegistered, metav1.ConditionFalse,
			"RegistrationFailed", err.Error())
		return ctrl.Result{RequeueAfter: r.RetryInterval}, err
	}
	setCondition(status, amtv1.ConditionRegistered, metav1.ConditionTrue,
		"Registered", "Machine and Hardware are in sync")
	status.Phase = amtv1.PhaseRegistered

	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// register writes the credential Secret, Machine and Hardware for a verified
// device.
func (r *Reconciler) register(
	ctx context.Context,
	device *amtv1.AMTDevice,
	status *amtv1.AMTDeviceStatus,
	conn *verified,
	inv *amt.Inventory,
	log *slog.Logger,
) error {
	name := device.Name
	secretName := name + "-amt-auth"
	own := ownership{platformGUID: conn.facts.PlatformGUID, deviceName: device.Name}
	app := &applier{client: r.Client, now: r.Now, log: log}

	secret := DesiredAuthSecret(secretName, device.Namespace, conn.credentials, "")
	if err := app.apply(ctx, "credentials secret", &corev1.Secret{}, secret, own, nil); err != nil {
		return fmt.Errorf("applying credentials secret %s: %w", secretName, err)
	}

	machine := DesiredMachine(name, device.Namespace,
		device.Spec.Endpoint.Host, int(device.Spec.Endpoint.Port),
		corev1.SecretReference{Name: secretName, Namespace: device.Namespace})
	if err := app.apply(ctx, "machine", &bmcv1.Machine{}, machine, own, nil); err != nil {
		return fmt.Errorf("applying machine %s: %w", name, err)
	}

	hardware := sync.DesiredHardware(inv.Device, sync.HardwareOptions{
		Name:           name,
		Namespace:      device.Namespace,
		FacilityCode:   r.FacilityCode,
		AutoEnrollment: r.AutoEnrollment,
	}, nil)
	// spec.interfaces is an SSA-atomic list, so the apply owns the whole
	// list and must carry the tink workflow controller's netboot state
	// forward inside it rather than resetting it to create-time defaults.
	carry := func(live client.Object) {
		lh, ok := live.(*tinkv1.Hardware)
		if !ok || len(hardware.Spec.Interfaces) == 0 {
			return
		}
		hardware.Spec.Interfaces[0].Netboot = sync.NetbootFor(lh)
	}
	if err := app.apply(ctx, "hardware", &tinkv1.Hardware{}, hardware, own, carry); err != nil {
		return fmt.Errorf("applying hardware %s: %w", name, err)
	}

	status.MachineRef = &corev1.LocalObjectReference{Name: name}
	status.HardwareRef = &corev1.LocalObjectReference{Name: name}
	return nil
}

// profileFor resolves the AMTProfile governing a device, falling back to the
// default profile name. A missing default is not an error: an empty profile
// is a usable policy, and failing here would make every device unmanageable
// because one cluster-scoped object was never created.
func (r *Reconciler) profileFor(ctx context.Context, device *amtv1.AMTDevice) (*amtv1.AMTProfile, error) {
	name := device.Spec.ProfileRef.Name
	if name == "" {
		name = r.DefaultProfile
	}

	profile := &amtv1.AMTProfile{}
	err := r.Client.Get(ctx, client.ObjectKey{Name: name}, profile)
	switch {
	case err == nil:
		return profile, nil
	case apierrors.IsNotFound(err) && name == r.DefaultProfile:
		r.Log.Debug("default AMTProfile not found; using an empty policy", "profile", name)
		return &amtv1.AMTProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}, nil
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("AMTProfile %q not found", name)
	default:
		return nil, fmt.Errorf("reading AMTProfile %q: %w", name, err)
	}
}

// patchStatus writes status only when it changed, so a steady-state resync
// does not generate a write per device per interval.
func (r *Reconciler) patchStatus(
	ctx context.Context,
	device *amtv1.AMTDevice,
	status *amtv1.AMTDeviceStatus,
) error {
	if equalStatus(&device.Status, status) {
		return nil
	}
	patched := device.DeepCopy()
	patched.Status = *status
	if err := r.Client.Status().Update(ctx, patched); err != nil {
		return fmt.Errorf("updating AMTDevice status: %w", err)
	}
	return nil
}
