package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ControlModePolicy selects which AMT control mode the fleet should aim for.
type ControlModePolicy string

const (
	// ControlModeBest takes Admin when the platform allows it, else Client.
	ControlModeBest ControlModePolicy = "Best"
	// ControlModeAdmin requires Admin Control Mode.
	ControlModeAdmin ControlModePolicy = "Admin"
	// ControlModeClient requires Client Control Mode.
	ControlModeClient ControlModePolicy = "Client"
)

// TLSMode selects how the controller treats the AMT device's TLS certificate.
type TLSMode string

const (
	// TLSModePin records the device certificate fingerprint on first verified
	// connection and refuses to connect if it later changes.
	TLSModePin TLSMode = "Pin"
	// TLSModeInsecure skips verification entirely. This is what bmclib does
	// today; it exists only as an escape hatch.
	TLSModeInsecure TLSMode = "Insecure"
)

// OptInPolicy mirrors IPS_OptInService.OptInRequired. Only settable in Admin
// Control Mode.
type OptInPolicy string

const (
	// OptInNone requires no user consent for redirection.
	OptInNone OptInPolicy = "None"
	// OptInKVM requires user consent for KVM only.
	OptInKVM OptInPolicy = "KVM"
	// OptInAll requires user consent for all redirection.
	OptInAll OptInPolicy = "All"
)

// PasswordPolicy governs generated AMT admin passwords.
//
// AMT enforces its own complexity rules in firmware: 8-32 characters with at
// least one upper, lower, digit and special character, drawn from a restricted
// special set. A password outside those rules is rejected by the device, so
// generation is validated before use.
type PasswordPolicy struct {
	// Length of generated passwords.
	// +kubebuilder:validation:Minimum=8
	// +kubebuilder:validation:Maximum=32
	// +kubebuilder:default=24
	// +optional
	Length int32 `json:"length,omitempty"`

	// Rotation is how often to rotate the admin password. Zero disables
	// rotation.
	//
	// Rotation writes the new password over WS-Man and then verifies it. The
	// old password is dead the moment the write lands, so the per-device
	// Secret retains previousPassword and the connection path tries both. A
	// half-failed rotation self-corrects on the next reconcile.
	// +optional
	Rotation *metav1.Duration `json:"rotation,omitempty"`
}

// TLSPolicy governs the controller's transport to AMT.
type TLSPolicy struct {
	// Mode selects certificate handling.
	// +kubebuilder:validation:Enum=Pin;Insecure
	// +kubebuilder:default=Pin
	// +optional
	Mode TLSMode `json:"mode,omitempty"`
}

// RedirectionPolicy governs AMT_RedirectionService.
//
// EnabledState and ListenerEnabled are independent: a device can report
// IDER+SOL enabled while the listener is off, in which case ports 16994/16995
// stay closed.
type RedirectionPolicy struct {
	// SOL enables Serial-over-LAN.
	// +optional
	SOL *bool `json:"sol,omitempty"`

	// IDER enables IDE redirection. Note that AMT 11 and later use USB-R for
	// the storage data plane; this flag only controls the firmware capability
	// bit, and this project does not implement a storage redirection client.
	// +optional
	IDER *bool `json:"ider,omitempty"`

	// ListenerEnabled opens the redirection listener. Without it SOL is
	// unreachable even when enabled.
	// +optional
	ListenerEnabled *bool `json:"listenerEnabled,omitempty"`
}

// VirtualMediaPolicy governs OCR UEFI HTTPS boot.
type VirtualMediaPolicy struct {
	// Enabled exposes Redfish VirtualMedia for devices whose firmware reports
	// ForceUEFIHTTPSBoot. Devices without it never get a VirtualMedia
	// resource.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// EnforceSecureBoot requires the boot image to be signed.
	// +kubebuilder:default=false
	// +optional
	EnforceSecureBoot *bool `json:"enforceSecureBoot,omitempty"`

	// ImageBaseURL is the HTTPS base URL of the boot image server. AMT fetches
	// the image itself, so this must be reachable from the device and present
	// a certificate AMT trusts.
	// +optional
	ImageBaseURL string `json:"imageBaseURL,omitempty"`
}

// AMTProfileSpec is fleet-wide AMT policy.
type AMTProfileSpec struct {
	// ControlMode is the control mode the fleet should aim for. It is
	// currently advisory: v1 does not activate devices, so the mode is
	// whatever onboarding produced. It is reported back in
	// AMTDevice.status.controlMode.
	// +kubebuilder:validation:Enum=Best;Admin;Client
	// +kubebuilder:default=Best
	// +optional
	ControlMode ControlModePolicy `json:"controlMode,omitempty"`

	// CredentialSources is an ordered list of Secrets holding candidate
	// username/password pairs. The controller pivots through them in order,
	// verifying each with a real WS-Man session, and records the pair that
	// worked in a per-device Secret.
	// +optional
	CredentialSources []corev1.SecretReference `json:"credentialSources,omitempty"`

	// Password governs generated admin passwords.
	// +optional
	Password PasswordPolicy `json:"password,omitempty"`

	// TLS governs the controller's transport to AMT.
	// +optional
	TLS TLSPolicy `json:"tls,omitempty"`

	// Redirection governs AMT_RedirectionService.
	// +optional
	Redirection RedirectionPolicy `json:"redirection,omitempty"`

	// Consent sets IPS_OptInService.OptInRequired. Ignored outside Admin
	// Control Mode.
	// +kubebuilder:validation:Enum=None;KVM;All
	// +optional
	Consent OptInPolicy `json:"consent,omitempty"`

	// TimeSync keeps AMT's clock aligned with the controller.
	// +kubebuilder:default=true
	// +optional
	TimeSync *bool `json:"timeSync,omitempty"`

	// VirtualMedia governs OCR UEFI HTTPS boot.
	// +optional
	VirtualMedia VirtualMediaPolicy `json:"virtualMedia,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=amtprofiles,shortName=amtp
// +kubebuilder:printcolumn:name="Control Mode",type=string,JSONPath=`.spec.controlMode`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AMTProfile is fleet-wide Intel AMT policy.
type AMTProfile struct {
	metav1.TypeMeta   `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AMTProfileSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AMTProfileList is a list of AMTProfile.
type AMTProfileList struct {
	metav1.TypeMeta `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AMTProfile `json:"items"`
}
