package upgrade

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	gateBlockedMetric = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_gate_blocked_total",
		Help: "Reconciles where the sync was gated, by reason (kubeconfig, talosconfig, convergence).",
	}, []string{"reason"})

	syncTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "upgrade_sync_total",
		Help: "Bootstrap-manifest sync attempts by result (success, error).",
	}, []string{"result"})

	lastSyncTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "upgrade_last_sync_timestamp_seconds",
		Help: "Unix time of the last successful bootstrap-manifest sync.",
	})

	manifestsPruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "upgrade_manifests_pruned_total",
		Help: "Bootstrap-manifest objects pruned across all syncs.",
	})
)

func init() {
	metrics.Registry.MustRegister(gateBlockedMetric, syncTotal, lastSyncTimestamp, manifestsPruned)
}
