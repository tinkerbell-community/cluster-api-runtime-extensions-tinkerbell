package redfish

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	amtv1 "github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/amt/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/internal/amtenroll"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

// KubeStore serves devices from AMTDevice resources.
//
// Reads never touch a device: the enrollment reconciler already wrote
// everything static into status, so rendering the fleet costs one cached list.
type KubeStore struct {
	Client    client.Client
	Namespace string
	Timeout   time.Duration
}

// List returns every registered device.
//
// Devices that have not yet been identified are skipped: without a platform
// GUID there is no stable id to key a Redfish resource on, and inventing one
// would produce resources whose URLs change underneath clients.
func (s *KubeStore) List(ctx context.Context) ([]Device, error) {
	list := &amtv1.AMTDeviceList{}
	if err := s.Client.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, fmt.Errorf("listing AMTDevices: %w", err)
	}

	out := make([]Device, 0, len(list.Items))
	for i := range list.Items {
		dev, ok := deviceFrom(&list.Items[i])
		if !ok {
			continue
		}
		out = append(out, dev)
	}
	return out, nil
}

// Get returns one device by platform GUID.
func (s *KubeStore) Get(ctx context.Context, id string) (Device, error) {
	devices, err := s.List(ctx)
	if err != nil {
		return Device{}, err
	}
	for _, d := range devices {
		if d.ID == id {
			return d, nil
		}
	}
	return Device{}, ErrNotFound
}

// Connect opens an authenticated AMT session for live operations.
func (s *KubeStore) Connect(ctx context.Context, id string) (Client, error) {
	resource, err := s.resourceFor(ctx, id)
	if err != nil {
		return nil, err
	}

	ref := resource.Spec.CredentialsRef
	if ref == nil || ref.Name == "" {
		return nil, fmt.Errorf("device %s has no credentials", id)
	}
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: resource.Namespace, Name: ref.Name}
	if err := s.Client.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("credentials secret %s for device %s is missing", ref.Name, id)
		}
		return nil, fmt.Errorf("reading credentials for device %s: %w", id, err)
	}

	c, err := amt.New(amt.Config{
		Host:     resource.Spec.Endpoint.Host,
		Port:     int(resource.Spec.Endpoint.Port),
		Username: string(secret.Data[amtenroll.SecretKeyUsername]),
		Password: string(secret.Data[amtenroll.SecretKeyPassword]),
		Timeout:  s.Timeout,
		// The fingerprint learned at enrollment is enforced here too: a
		// device whose certificate changed is refused rather than driven.
		PinnedFingerprint: resource.Status.TLSFingerprint,
	})
	if err != nil {
		return nil, err
	}
	return &amtClient{client: c}, nil
}

func (s *KubeStore) resourceFor(ctx context.Context, id string) (*amtv1.AMTDevice, error) {
	list := &amtv1.AMTDeviceList{}
	if err := s.Client.List(ctx, list, client.InNamespace(s.Namespace)); err != nil {
		return nil, fmt.Errorf("listing AMTDevices: %w", err)
	}
	for i := range list.Items {
		if list.Items[i].Status.Inventory == nil {
			continue
		}
		if platformGUIDOf(&list.Items[i]) == id {
			return &list.Items[i], nil
		}
	}
	return nil, ErrNotFound
}

// platformGUIDOf returns the stable id for a device resource.
func platformGUIDOf(d *amtv1.AMTDevice) string {
	return d.Spec.Identity.PlatformGUID
}

// deviceFrom projects an AMTDevice onto the aggregator's view, reporting
// whether it can be served at all.
func deviceFrom(d *amtv1.AMTDevice) (Device, bool) {
	id := platformGUIDOf(d)
	if id == "" {
		return Device{}, false
	}

	dev := Device{
		ID:          id,
		Name:        d.Name,
		Host:        d.Spec.Endpoint.Host,
		AMTVersion:  d.Status.Firmware.AMT,
		ControlMode: d.Status.ControlMode,
		Phase:       string(d.Status.Phase),
		Reachable:   d.Status.Phase == amtv1.PhaseRegistered || d.Status.Phase == amtv1.PhaseVerified,
		Capabilities: Capabilities{
			PXE:       d.Status.BootCapabilities.PXE,
			HardDrive: d.Status.BootCapabilities.HardDrive,
			CD:        d.Status.BootCapabilities.CDDVD,
			UEFIHTTPS: d.Status.BootCapabilities.UEFIHTTPS,
		},
	}

	if inv := d.Status.Inventory; inv != nil {
		dev.Manufacturer = inv.Manufacturer
		dev.Model = inv.Model
		dev.SerialNumber = inv.SerialNumber
		dev.BaseboardModel = inv.BaseboardModel
		dev.BaseboardSerial = inv.BaseboardSerial
		dev.BIOSVersion = inv.BIOSVersion
		dev.CPUCount = int(inv.CPUCount)
		dev.CPUModel = inv.CPUModel
		dev.MemoryBytes = inv.MemoryBytes
		for _, n := range inv.NICs {
			dev.NICs = append(dev.NICs, NIC{MACAddress: n.MACAddress, Name: n.Name})
		}
	}
	return dev, true
}

// amtClient adapts pkg/amt to the aggregator's Client interface, translating
// Redfish vocabulary into the package's typed values.
type amtClient struct {
	client *amt.Client
}

func (c *amtClient) PowerState(ctx context.Context) (string, error) {
	state, err := c.client.PowerState(ctx)
	return string(state), err
}

func (c *amtClient) Reset(ctx context.Context, resetType string) error {
	return c.client.Reset(ctx, amt.ResetType(resetType))
}

func (c *amtClient) SetBootOverride(ctx context.Context, target string) error {
	return c.client.SetBootOverride(ctx, amt.BootTarget(target))
}

func (c *amtClient) ClearBootOverride(ctx context.Context) error {
	return c.client.ClearBootOverride(ctx)
}

func (c *amtClient) InsertVirtualMedia(ctx context.Context, imageURL string, enforceSecureBoot bool) error {
	return c.client.InsertVirtualMedia(ctx, imageURL, enforceSecureBoot)
}

func (c *amtClient) EjectVirtualMedia(ctx context.Context) error {
	return c.client.EjectVirtualMedia(ctx)
}

// Close is a no-op: WS-Man is stateless over HTTP digest, so there is no
// session to tear down. The method exists so callers can treat the client as
// a resource and not have to know that.
func (c *amtClient) Close() error { return nil }
