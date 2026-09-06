// Package v1alpha1 holds the schema-only variable roots for the
// cluster-api-runtime-extensions-tinkerbell topology variables (spec §4.3,
// following caren-analysis.md §2): kubebuilder-marked types whose generated CRD
// YAML is embedded and converted to ClusterClass variable schema at init — the
// CRDs are NEVER installed. Defaults, enums, patterns, and CEL XValidations
// come free from core CAPI's variable machinery; this repo carries zero
// variable-webhook code.
//
// The variable surface is deliberately minimal (version + endpoint): under the
// decided Strategy B the installer image is per-machine via the Hardware
// annotation, so no schematic variable exists, and every field kept out of a
// variable is a field that cannot need `yq` anyOf surgery (spec §4.3).
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TalosConfig are the Talos-specific cluster variables.
type TalosConfig struct {
	// version is the Talos OS version the cluster tracks, e.g. "v1.13.9" (full
	// pin), "v1.13" (track the minor's latest GA patch), or "latest". It drives
	// both provisioning and in-place upgrades — changing it IS the supported
	// upgrade trigger, so it is deliberately mutable (no self == oldSelf CEL).
	// +kubebuilder:validation:Pattern=`^(latest|v[0-9]+\.[0-9]+(\.[0-9]+)?([-+].+)?)$`
	// +optional
	Version string `json:"version,omitempty"`
}

// ControlPlaneEndpoint is the workload cluster's stable API endpoint (the VIP),
// mutated into TinkerbellCluster by the topology patch.
type ControlPlaneEndpoint struct {
	// host is the VIP or DNS name of the control-plane endpoint.
	// +kubebuilder:validation:MinLength=1
	// +required
	Host string `json:"host"`

	// port of the control-plane endpoint.
	// +kubebuilder:default=6443
	// +optional
	Port int32 `json:"port,omitempty"`
}

// TalosClusterConfigSpec is the schema of the required `clusterConfig`
// topology variable.
type TalosClusterConfigSpec struct {
	// talos carries the Talos-specific cluster settings.
	// +optional
	Talos *TalosConfig `json:"talos,omitempty"`

	// controlPlaneEndpoint is the stable API endpoint of the workload cluster.
	// +required
	ControlPlaneEndpoint ControlPlaneEndpoint `json:"controlPlaneEndpoint"`
}

// TalosClusterConfig is the variable root. Never installed; schema-only.
// +kubebuilder:object:root=true
type TalosClusterConfig struct {
	metav1.TypeMeta   `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec TalosClusterConfigSpec `json:"spec,omitempty"`
}

// TalosWorkerNodeConfigSpec is the schema of the optional `workerConfig`
// topology variable; per-MachineDeployment differences ride CAPI-native
// MD-level variable overrides.
type TalosWorkerNodeConfigSpec struct {
	// talos carries the Talos-specific worker settings.
	// +optional
	Talos *TalosConfig `json:"talos,omitempty"`
}

// TalosWorkerNodeConfig is the variable root. Never installed; schema-only.
// +kubebuilder:object:root=true
type TalosWorkerNodeConfig struct {
	metav1.TypeMeta   `json:",inline"` //nolint:revive // kubebuilder-standard inline TypeMeta
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec TalosWorkerNodeConfigSpec `json:"spec,omitempty"`
}
