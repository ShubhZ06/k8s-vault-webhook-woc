package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// MutationsTotal tracks the total number of pod mutations attempted
	MutationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8sv_webhook_mutations_total",
			Help: "Total number of pod mutations attempted by the k8s-vault-webhook",
		},
		[]string{"provider", "namespace", "result"},
	)

	// AdmissionDuration tracks the duration of the entire admission webhook handler
	AdmissionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "k8sv_webhook_admission_duration_seconds",
			Help:    "Duration of the admission webhook handler in seconds",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
		},
		[]string{"namespace"},
	)

	// RegistryLookupsTotal tracks the total number of image registry lookups
	RegistryLookupsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8sv_webhook_registry_lookups_total",
			Help: "Total number of image registry lookups for CMD/ENTRYPOINT resolution",
		},
		[]string{"registry", "result"},
	)

	// RegistryCacheHits tracks image config cache hits
	RegistryCacheHits = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "k8sv_webhook_registry_cache_hits_total",
			Help: "Total number of image config cache hits",
		},
	)

	// RegistryCacheMisses tracks image config cache misses
	RegistryCacheMisses = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "k8sv_webhook_registry_cache_misses_total",
			Help: "Total number of image config cache misses",
		},
	)

	// ExcludedPodsTotal tracks pods skipped due to namespace or annotation exclusion
	ExcludedPodsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "k8sv_webhook_excluded_pods_total",
			Help: "Total number of pods skipped due to exclusion rules",
		},
		[]string{"namespace", "reason"},
	)
)

// InitMetrics registers all custom Prometheus metrics with the default registerer
func InitMetrics() {
	prometheus.MustRegister(MutationsTotal)
	prometheus.MustRegister(AdmissionDuration)
	prometheus.MustRegister(RegistryLookupsTotal)
	prometheus.MustRegister(RegistryCacheHits)
	prometheus.MustRegister(RegistryCacheMisses)
	prometheus.MustRegister(ExcludedPodsTotal)
}
