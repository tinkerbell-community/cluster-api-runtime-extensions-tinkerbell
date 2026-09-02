package janitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	scrubbedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "janitor_hardware_scrubbed_total",
		Help: "Released Hardware objects scrubbed.",
	})
	updateConflicts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "janitor_update_conflicts_total",
		Help: "Scrub updates rejected by the resourceVersion guard (concurrent claim or write).",
	})
	halfReleased = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "janitor_half_released_hardware",
		Help: "Hardware currently in the half-released anomaly state (owner labels absent, userData present, provisioned annotation present); needs operator attention.",
	}, []string{"namespace", "hardware"})
)

func init() {
	metrics.Registry.MustRegister(scrubbedTotal, updateConflicts, halfReleased)
}
