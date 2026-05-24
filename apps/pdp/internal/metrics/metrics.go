// Package metrics registers the Prometheus metrics for the PDP service.
// All metrics are pre-registered at init time so there is no per-request
// registration overhead.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	DecisionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pdp_decision_total",
		Help: "Total number of authorization decisions made.",
	}, []string{"effect", "cache", "tenant"})

	DecisionDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pdp_decision_duration_seconds",
		Help:    "Latency of authorization decisions.",
		Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25},
	}, []string{"cache"})

	CompileDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "pdp_compile_duration_seconds",
		Help:    "Time to compile a policy closure from AST.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5},
	})

	InvalidateTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pdp_invalidate_total",
		Help: "Cache invalidation events received.",
	}, []string{"result"})

	ActivePolicies = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pdp_active_policies_total",
		Help: "Number of active compiled policies per tenant.",
	}, []string{"tenant"})

	CompilePanicTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pdp_compile_panic_total",
		Help: "Number of panics recovered during policy closure compilation.",
	})

	CacheL1Size = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pdp_cache_l1_size",
		Help: "Current number of entries in the L1 decision cache.",
	})

	RedisDown = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pdp_redis_down",
		Help: "1 if the Redis L2 cache is currently unreachable, 0 otherwise.",
	})
)
