package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phase is the coarse enrollment state of an AMT device.
type Phase string

const (
	// PhasePending means the device is known but nothing has been verified.
	PhasePending Phase = "Pending"
	// PhaseUnprovisioned means AMT is reachable but sits in PreProvisioning:
	// it has no admin credential and must be onboarded out of band.
	PhaseUnprovisioned Phase = "Unprovisioned"
	// PhaseVerified means a credential pair authenticated over WS-Man.
	PhaseVerified Phase = "Verified"
	// PhaseRegistered means Machine and Hardware exist for this device.
	PhaseRegistered Phase = "Registered"
	// PhaseFailed means the device could not be reached or verified.
	PhaseFailed Phase = "Failed"
)

// Condition types reported on an AMTDevice.
const (
	// ConditionReachable is true when the AMT WS-Man endpoint answers.
	ConditionReachable = "Reachable"
	// ConditionProvisioned is false when AMT is in PreProvisioning and needs
	// out-of-band onboarding.
	ConditionProvisioned = "Provisioned"
	// ConditionCredentialsVerified is true when a credential pair
	// authenticated.
	ConditionCredentialsVerified = "CredentialsVerified"
	// ConditionInventoryCollected is true when CIM inventory was read.
	ConditionInventoryCollected = "InventoryCollected"
	// ConditionRegistered is true when Machine and Hardware are in sync.
	ConditionRegistered = "Registered"
)

// Endpoint locates the AMT WS-Man service.
type Endpoint struct {
	// Host is the device address.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// Port is the WS-Man port. 16993 is TLS, 16992 plaintext.
	// +kubebuilder:default=16993
	// +optional
	Port int32 `json:"port,omitempty"`

	// Scheme is http or https.
	// +kubebuilder:validation:Enum=http;https
	// +kubebuilder:default=https
	// +optional
	Scheme string `json:"scheme,omitempty"`
}

// Identity holds the stable identifiers for a device.
type Identity struct {
	// PlatformGUID from CIM_ComputerSystemPackage. This is the stable identity
	// across reboots, address changes and renames, and keys every Redfish
	// resource.
	// +optional
	PlatformGUID string `json:"platformGUID,omitempty"`

	// MACAddresses of the host NICs, lower-case colon-separated.
	// +optional
	MACAddresses []string `json:"macAddresses,omitempty"`
}

// FirmwareInfo reports AMT firmware versions from CIM_SoftwareIdentity.
type FirmwareInfo struct {
	// +optional
	AMT string `json:"amt,omitempty"`
	// +optional
	Build string `json:"build,omitempty"`
	// +optional
	SKU string `json:"sku,omitempty"`
	// +optional
	Recovery string `json:"recovery,omitempty"`
}

// BootCapabilities mirrors AMT_BootCapabilities. It is observed, never
// desired: it gates what the Redfish aggregator advertises for this device.
type BootCapabilities struct {
	// +optional
	PXE bool `json:"pxe,omitempty"`
	// +optional
	HardDrive bool `json:"hardDrive,omitempty"`
	// +optional
	CDDVD bool `json:"cdDVD,omitempty"`
	// UEFIHTTPS reports ForceUEFIHTTPSBoot. Redfish VirtualMedia is advertised
	// only when this is true.
	// +optional
	UEFIHTTPS bool `json:"uefiHTTPS,omitempty"`
	// +optional
	IDER bool `json:"ider,omitempty"`
	// +optional
	SOL bool `json:"sol,omitempty"`
	// +optional
	BIOSSetup bool `json:"biosSetup,omitempty"`
	// +optional
	SecureBootControl bool `json:"secureBootControl,omitempty"`
}

// RedirectionStatus mirrors AMT_RedirectionService.
type RedirectionStatus struct {
	// EnabledState is the raw AMT value: 32768 disabled, 32769 IDER only,
	// 32770 SOL only, 32771 both.
	// +optional
	EnabledState int32 `json:"enabledState,omitempty"`

	// ListenerEnabled reports whether the redirection listener is open. When
	// false, ports 16994/16995 are closed regardless of EnabledState.
	// +optional
	ListenerEnabled bool `json:"listenerEnabled,omitempty"`
}

// InventorySummary is a digest of the collected CIM inventory, enough to
// render Redfish without a live device call.
type InventorySummary struct {
	// +optional
	Manufacturer string `json:"manufacturer,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
	// +optional
	SerialNumber string `json:"serialNumber,omitempty"`
	// +optional
	BaseboardModel string `json:"baseboardModel,omitempty"`
	// +optional
	BaseboardSerial string `json:"baseboardSerial,omitempty"`
	// +optional
	BIOSVersion string `json:"biosVersion,omitempty"`
	// +optional
	CPUCount int32 `json:"cpuCount,omitempty"`
	// +optional
	CPUModel string `json:"cpuModel,omitempty"`
	// +optional
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
	// +optional
	NICs []NIC `json:"nics,omitempty"`
}

// NIC is one host network interface as reported by CIM_EthernetPort.
type NIC struct {
	// +optional
	MACAddress string `json:"macAddress,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
}

// AMTDeviceSpec is the desired state for one AMT device.
type AMTDeviceSpec struct {
	// ProfileRef names the cluster-scoped AMTProfile governing this device.
	// +kubebuilder:default={name: default}
	// +optional
	ProfileRef corev1.LocalObjectReference `json:"profileRef,omitempty"`

	// Endpoint locates the WS-Man service.
	Endpoint Endpoint `json:"endpoint"`

	// Identity holds stable identifiers. Fields left empty are discovered.
	// +optional
	Identity Identity `json:"identity,omitempty"`

	// CredentialsRef names the Secret holding the verified username, password
	// and previousPassword for this device. Created by the controller once a
	// candidate pair authenticates.
	// +optional
	CredentialsRef *corev1.LocalObjectReference `json:"credentialsRef,omitempty"`
}

// AMTDeviceStatus is the observed state of one AMT device.
type AMTDeviceStatus struct {
	// +optional
	Phase Phase `json:"phase,omitempty"`

	// ControlMode is the observed AMT control mode: Admin, Client or
	// NotProvisioned.
	// +optional
	ControlMode string `json:"controlMode,omitempty"`

	// AllowedControlModes reports which modes host-based setup may enter.
	// Recorded because it determines whether cert-free Admin activation is
	// possible on this platform.
	// +optional
	AllowedControlModes []string `json:"allowedControlModes,omitempty"`

	// ProvisioningState is PreProvisioning, InProvisioning or
	// PostProvisioning.
	// +optional
	ProvisioningState string `json:"provisioningState,omitempty"`

	// +optional
	Firmware FirmwareInfo `json:"firmware,omitempty"`

	// +optional
	BootCapabilities BootCapabilities `json:"bootCapabilities,omitempty"`

	// +optional
	Redirection RedirectionStatus `json:"redirection,omitempty"`

	// TLSFingerprint is the pinned SHA-256 of the device certificate. Once
	// set, a mismatch is a hard connection failure.
	// +optional
	TLSFingerprint string `json:"tlsFingerprint,omitempty"`

	// DigestRealm from AMT_GeneralSettings, needed to compute the digest
	// password for rotation.
	// +optional
	DigestRealm string `json:"digestRealm,omitempty"`

	// +optional
	Inventory *InventorySummary `json:"inventory,omitempty"`

	// +optional
	MachineRef *corev1.LocalObjectReference `json:"machineRef,omitempty"`

	// +optional
	HardwareRef *corev1.LocalObjectReference `json:"hardwareRef,omitempty"`

	// LastVerifiedTime is when a credential pair last authenticated.
	// +optional
	LastVerifiedTime *metav1.Time `json:"lastVerifiedTime,omitempty"`

	// LastRotatedTime is when the admin password was last rotated.
	// +optional
	LastRotatedTime *metav1.Time `json:"lastRotatedTime,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=amtdevices,shortName=amtd
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.spec.endpoint.host`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.status.controlMode`
// +kubebuilder:printcolumn:name="AMT",type=string,JSONPath=`.status.firmware.amt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AMTDevice is an Intel AMT managed device.
type AMTDevice struct {
	metav1.TypeMeta   `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AMTDeviceSpec   `json:"spec,omitempty"`
	Status AMTDeviceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AMTDeviceList is a list of AMTDevice.
type AMTDeviceList struct {
	metav1.TypeMeta `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AMTDevice `json:"items"`
}
