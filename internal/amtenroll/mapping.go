// Package amtenroll reconciles AMTDevice resources: it verifies credentials
// against the device, collects facts and inventory, and registers the device
// as a Tinkerbell Machine and Hardware.
package amtenroll

import (
	bmcv1 "github.com/tinkerbell/tinkerbell/api/v1alpha1/bmc"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/amt"
)

const (
	// ManagedByLabel marks resources owned by bmc-manager. Resources without
	// it are never modified or adopted.
	//
	// It is deliberately distinct from the discovery controller's label:
	// AMT devices are not discovered over mDNS, so the two components manage
	// disjoint sets of machines and neither may adopt the other's resources.
	ManagedByLabel = "amt.tinkerbell.org/managed-by"
	// ManagedByValue is the value of ManagedByLabel on owned resources.
	ManagedByValue = "bmc-manager"
	// FieldManager is the server-side-apply field manager for every write
	// this component makes. It equals ManagedByValue per the repo naming
	// standard: SSA manager identity == component name
	// (docs/discovery-field-ownership.md).
	FieldManager = ManagedByValue
	// LastSeenAnnotation records the last successful reconcile (RFC3339).
	LastSeenAnnotation = "amt.tinkerbell.org/last-seen"
	// PlatformGUIDAnnotation records the device's stable AMT platform GUID.
	PlatformGUIDAnnotation = "amt.tinkerbell.org/platform-guid"
	// DeviceAnnotation records the AMTDevice this resource was built from.
	DeviceAnnotation = "amt.tinkerbell.org/device"
)

// Secret data keys on the per-device credential Secret.
const (
	// SecretKeyUsername is the AMT account name.
	SecretKeyUsername = "username"
	// SecretKeyPassword is the current AMT password.
	SecretKeyPassword = "password"
	// SecretKeyPreviousPassword is the password in use before the most recent
	// rotation.
	//
	// Rotation cannot be transactional: the old password stops working the
	// instant the write lands, so a failure to verify afterwards is
	// ambiguous. Retaining the previous value lets the connection path try
	// both and lets a half-failed rotation self-correct on the next
	// reconcile instead of locking the controller out of the device.
	SecretKeyPreviousPassword = "previousPassword"
)

// Credentials are an AMT account's username and password.
type Credentials struct {
	Username string
	Password string
}

// DesiredAuthSecret builds the sparse per-device credential Secret.
//
// previous may be empty, in which case no previousPassword key is written.
func DesiredAuthSecret(name, namespace string, creds Credentials, previous string) *corev1.Secret {
	data := map[string][]byte{
		SecretKeyUsername: []byte(creds.Username),
		SecretKeyPassword: []byte(creds.Password),
	}
	if previous != "" {
		data[SecretKeyPreviousPassword] = []byte(previous)
	}
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
}

// DesiredMachine builds the sparse bmc.tinkerbell.org Machine for an AMT
// device.
//
// The provider is pinned to IntelAMT rather than left to bmclib's default
// order: the default tries ipmitool and gofish first, and an AMT device
// answers neither, so every power action would pay their timeouts before
// reaching the provider that works.
//
// InsecureTLS is set because AMT ships a self-signed certificate that no
// chain can validate. bmclib has no certificate pinning, so this is as good
// as its transport gets; the controller's own connections pin the
// fingerprint instead (see pkg/amt).
func DesiredMachine(name, namespace, host string, port int, authRef corev1.SecretReference) *bmcv1.Machine {
	scheme := "https"
	if port == amt.PortPlaintext {
		scheme = "http"
	}
	return &bmcv1.Machine{
		TypeMeta:   metav1.TypeMeta{APIVersion: bmcv1.GroupVersion.String(), Kind: "Machine"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: bmcv1.MachineSpec{
			Connection: bmcv1.Connection{
				Host:          host,
				Port:          port,
				AuthSecretRef: authRef,
				InsecureTLS:   true,
				ProviderOptions: &bmcv1.ProviderOptions{
					PreferredOrder: []bmcv1.ProviderName{"IntelAMT"},
					IntelAMT: &bmcv1.IntelAMTOptions{
						Port:       port,
						HostScheme: scheme,
					},
				},
			},
		},
	}
}
