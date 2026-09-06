package upgrade

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// clusterKind is the owner kind that names the CAPI cluster.
const clusterKind = "Cluster"

// TCPVersion returns spec.version (the target Kubernetes version). It is the one
// field the coordinator cannot proceed without.
func TCPVersion(u *unstructured.Unstructured) (string, error) {
	v, found, err := unstructured.NestedString(u.Object, "spec", "version")
	if err != nil || !found || v == "" {
		return "", fmt.Errorf("TalosControlPlane %s/%s has no spec.version: %w", u.GetNamespace(), u.GetName(), err)
	}
	return v, nil
}

// TCPTalosVersion returns spec.controlPlaneConfig.controlplane.talosVersion, or ""
// when unset (pre-P1 clusters). It is advisory only — the observed Talos version
// from the live node is authoritative for the pair.
func TCPTalosVersion(u *unstructured.Unstructured) string {
	v, _, _ := unstructured.NestedString(u.Object, "spec", "controlPlaneConfig", "controlplane", "talosVersion")
	return v
}

// LastSynced returns the checkpoint annotation value, or "".
func LastSynced(u *unstructured.Unstructured) string {
	return u.GetAnnotations()[LastSyncedAnnotation]
}

// ForceSyncRequested reports whether a human requested a one-shot gate bypass.
func ForceSyncRequested(u *unstructured.Unstructured) bool {
	return u.GetAnnotations()[ForceSyncAnnotation] == "true"
}

// ClusterNameFor resolves the owning CAPI cluster's name: the owner Cluster
// reference first (CACPPT's own resolution pattern), the cluster-name label as a
// fallback. The name keys the kubeconfig/talosconfig secrets and the Machine
// label selector, so failing to resolve it fails the reconcile.
func ClusterNameFor(u *unstructured.Unstructured) (string, error) {
	for _, ref := range u.GetOwnerReferences() {
		if ref.Kind == clusterKind && strings.HasPrefix(ref.APIVersion, "cluster.x-k8s.io/") {
			return ref.Name, nil
		}
	}
	if name := u.GetLabels()["cluster.x-k8s.io/cluster-name"]; name != "" {
		return name, nil
	}
	return "", fmt.Errorf("TalosControlPlane %s/%s has no owner Cluster reference and no cluster-name label", u.GetNamespace(), u.GetName())
}
