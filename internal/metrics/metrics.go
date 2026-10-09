package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	ScalingActionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "idlescaler",
			Name:      "scaling_actions_total",
			Help:      "Total count of scaling actions executed by the controller.",
		},
		[]string{"namespace", "target_name", "action", "result"},
	)
	TriggerCheckErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "idlescaler",
			Name:      "trigger_check_errors_total",
			Help:      "Total count of errors encountered when evaluating triggers.",
		},
		[]string{"namespace", "target_name", "trigger_type"},
	)
)

func init() {
	metrics.Registry.MustRegister(ScalingActionsTotal, TriggerCheckErrorsTotal)
}
