// Package talos serves the topology-mutation handlers of
// cluster-api-runtime-extensions-tinkerbell (spec §4.3, §4.4). Handler names
// are baked into every shipped ClusterClass, so they are versioned constants:
// renaming a referenced handler bricks topology reconciliation.
package talos

import (
	"context"

	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimehooksv1 "sigs.k8s.io/cluster-api/api/runtime/hooks/v1alpha1"

	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/api/v1alpha1"
	"github.com/tinkerbell-community/cluster-api-runtime-extensions-tinkerbell/pkg/variables"
)

const (
	// DiscoverVariablesHandlerName is the -dv handler; its name stays stable
	// forever (spec §4.4 "keep -dv names stable").
	DiscoverVariablesHandlerName = "talostinkerbellclusterconfigvars-dv"

	// ClusterConfigVariableName is the required cluster-level variable.
	ClusterConfigVariableName = "clusterConfig"
	// WorkerConfigVariableName is the optional worker variable; per-MD
	// differences ride CAPI-native MD-level overrides.
	WorkerConfigVariableName = "workerConfig"
)

// clusterConfigSchema and workerConfigSchema are converted once at init from
// the embedded, never-installed CRDs; a conversion defect fails the build's
// tests, not a live discovery call.
var (
	clusterConfigSchema = variables.MustSchemaFromCRDYAML(v1alpha1.TalosClusterConfigCRD)
	workerConfigSchema  = variables.MustSchemaFromCRDYAML(v1alpha1.TalosWorkerNodeConfigCRD)
)

// VariablesHandler serves DiscoverVariables.
type VariablesHandler struct{}

// DiscoverVariables publishes the clusterConfig (required) and workerConfig
// (optional) variable schemas.
func (h *VariablesHandler) DiscoverVariables(_ context.Context, _ *runtimehooksv1.DiscoverVariablesRequest, resp *runtimehooksv1.DiscoverVariablesResponse) {
	resp.Variables = []clusterv1.ClusterClassVariable{
		{
			Name:     ClusterConfigVariableName,
			Required: ptr.To(true),
			Schema:   clusterConfigSchema,
		},
		{
			Name:     WorkerConfigVariableName,
			Required: ptr.To(false),
			Schema:   workerConfigSchema,
		},
	}
	resp.SetStatus(runtimehooksv1.ResponseStatusSuccess)
}
