package upgrade

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// Nudger pushes an immediate coordinator reconcile for a cluster's
// TalosControlPlane — the lifecycle-hook fold-in (SP-10): topology upgrade
// milestones become event-driven nudges instead of waiting for a Machine watch
// transition or the blocked-gate poll. The nudge only accelerates observation;
// the convergence gate still decides whether anything syncs.
type Nudger struct {
	Client client.Reader
	// Events feeds the coordinator's WatchesRawSource channel.
	Events chan event.GenericEvent
}

// NudgeCluster resolves the cluster's controlPlaneRef and enqueues its
// TalosControlPlane. A cluster without a Talos control plane is not an error —
// this extension may serve mixed fleets.
func (n *Nudger) NudgeCluster(ctx context.Context, cluster client.ObjectKey) (int32, error) {
	c := &clusterv1.Cluster{}
	if err := n.Client.Get(ctx, cluster, c); err != nil {
		return 0, fmt.Errorf("reading cluster %s: %w", cluster, err)
	}
	ref := c.Spec.ControlPlaneRef
	if ref.Kind != TalosControlPlaneGVK.Kind || ref.Name == "" {
		return 0, nil
	}
	tcp := &unstructured.Unstructured{}
	tcp.SetGroupVersionKind(TalosControlPlaneGVK)
	tcp.SetNamespace(cluster.Namespace)
	tcp.SetName(ref.Name)
	select {
	case n.Events <- event.GenericEvent{Object: tcp}:
	default:
		// A full channel means a reconcile is already pending; dropping the
		// nudge loses nothing (level-triggered controller).
	}
	return 0, nil
}
