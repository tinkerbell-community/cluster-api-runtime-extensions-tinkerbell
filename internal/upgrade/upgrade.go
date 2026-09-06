// Package upgrade implements talos-upgrade-coordinator (C5): the cluster-scoped
// remainder of `talosctl upgrade-k8s` — server-side-apply synchronization of the
// Talos bootstrap manifests into the workload cluster (with pruning), gated on
// actual cluster-wide version convergence. It is read-only toward CAPI apart from
// one checkpoint annotation, and it never drives a machine-level action.
// See docs/talos-upgrade-coordinator.md.
package upgrade

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// Name is the component name: binary, chart, image, event recorder, and the
	// management-cluster field manager. Workload-cluster manifest writes use the
	// foreign `talos` manager instead (shared with talosctl — see the Decision in
	// the component doc).
	Name = "talos-upgrade-coordinator"

	// FieldManager attributes management-cluster metadata writes.
	FieldManager = Name

	// LastSyncedAnnotation checkpoints the last successfully synced version pair
	// on the TalosControlPlane. Written only after a fully successful apply.
	LastSyncedAnnotation = "upgrade.tinkerbell.org/last-synced-version"

	// ForceSyncAnnotation ("true") is a human-set one-shot gate bypass, consumed
	// and removed by the coordinator after honoring it.
	ForceSyncAnnotation = "upgrade.tinkerbell.org/force-sync"
)

// TalosControlPlaneGVK identifies the control-plane object, accessed as
// unstructured: three scalar fields and annotations are not worth a module
// dependency on the control-plane provider.
var TalosControlPlaneGVK = schema.GroupVersionKind{
	Group:   "controlplane.cluster.x-k8s.io",
	Version: "v1beta1",
	Kind:    "TalosControlPlane",
}

// Pair is the desired (Kubernetes, Talos) version pair a sync is keyed on.
type Pair struct {
	K8s   string
	Talos string
}

// String renders the annotation value format, e.g. "k8s=v1.36.4,talos=v1.13.9".
func (p Pair) String() string {
	return fmt.Sprintf("k8s=%s,talos=%s", p.K8s, p.Talos)
}
