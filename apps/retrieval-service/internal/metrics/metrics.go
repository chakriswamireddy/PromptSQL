package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	SnapshotDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "retrieval_snapshot_duration_seconds",
		Help:    "Latency of AllowedSnapshot builds.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"tenant_id", "cache_hit"})

	DocsDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "retrieval_docs_duration_seconds",
		Help:    "Latency of doc-retrieval requests.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"tenant_id", "cache_hit"})

	CacheHits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retrieval_cache_hits_total",
		Help: "Number of cache hits per cache type.",
	}, []string{"cache_type"})

	CacheMisses = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retrieval_cache_misses_total",
		Help: "Number of cache misses per cache type.",
	}, []string{"cache_type"})

	InjectionTriggers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retrieval_injection_defense_triggers_total",
		Help: "Injection defense events by defense type.",
	}, []string{"defense_type", "tenant_id"})

	LLMRouteDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retrieval_llm_route_total",
		Help: "LLM routing decisions by provider and classification.",
	}, []string{"provider", "classification", "outcome"})

	SnapshotTruncations = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retrieval_snapshot_truncations_total",
		Help: "Number of snapshots that exceeded the token budget and were truncated.",
	}, []string{"tenant_id"})

	QuarantineReleasedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "retrieval_quarantine_released_total",
		Help: "Total doc_chunks released from quarantine by the sweeper.",
	})

	SnapshotSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "retrieval_snapshot_bytes",
		Help:    "Serialized AllowedSnapshot size in bytes.",
		Buckets: []float64{1024, 4096, 16384, 65536, 131072, 262144},
	}, []string{"tenant_id"})
)

func Register() {
	prometheus.MustRegister(
		SnapshotDuration,
		DocsDuration,
		CacheHits,
		CacheMisses,
		InjectionTriggers,
		LLMRouteDecisions,
		SnapshotTruncations,
		QuarantineReleasedTotal,
		SnapshotSize,
	)
}
