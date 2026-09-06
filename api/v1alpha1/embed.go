package v1alpha1

import _ "embed"

// The generated CRD manifests, embedded so the DiscoverVariables handler can
// convert them to ClusterClass variable schema at init. They are NEVER
// installed into a cluster (caren-analysis.md §2 "schema-only CRDs").
var (
	//go:embed crds/variables.runtime.tinkerbell.org_talosclusterconfigs.yaml
	TalosClusterConfigCRD []byte

	//go:embed crds/variables.runtime.tinkerbell.org_talosworkernodeconfigs.yaml
	TalosWorkerNodeConfigCRD []byte
)
