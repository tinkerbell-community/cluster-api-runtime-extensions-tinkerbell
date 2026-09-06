package resolve

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	identitiesResolved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_identities_resolved_total",
		Help: "Image identities applied onto claimed Hardware.",
	})
	versionsUnresolved = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_versions_unresolved_total",
		Help: "Reconciles that resolved no Talos version and wrote no operating_system.",
	})
	applyConflicts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "resolver_apply_conflicts_total",
		Help: "Sparse applies rejected by the resourceVersion precondition (concurrent write or release).",
	})
)

func init() {
	metrics.Registry.MustRegister(identitiesResolved, versionsUnresolved, applyConflicts)
}
