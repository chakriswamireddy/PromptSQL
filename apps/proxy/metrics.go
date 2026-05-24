package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	proxyConnectionsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proxy_connections_active",
		Help: "Number of active proxy client connections.",
	}, []string{"tenant_id"})

	proxyQueryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_queries_total",
		Help: "Total queries handled by the proxy.",
	}, []string{"tenant_id", "decision", "statement_type"})

	proxyQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_query_duration_seconds",
		Help:    "End-to-end query duration (client in → client out) in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant_id", "decision"})

	proxyPDPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_pdp_duration_seconds",
		Help:    "Time spent calling PDP BulkDecide.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant_id", "cache"})

	proxyRewriteDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_rewrite_duration_seconds",
		Help:    "Time spent in Calcite SQL rewrite.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant_id"})

	proxyBackendDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_backend_duration_seconds",
		Help:    "Time spent executing query on backend PostgreSQL.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant_id", "data_source_id"})

	proxyRowsStreamed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_rows_streamed_total",
		Help: "Total rows streamed to clients.",
	}, []string{"tenant_id", "data_source_id"})

	proxyRowsMasked = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_rows_masked_total",
		Help: "Total column values masked before streaming.",
	}, []string{"tenant_id"})

	proxyCostGateTrips = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_cost_gate_trips_total",
		Help: "Queries rejected by the cost gate.",
	}, []string{"tenant_id"})

	proxyUnsupportedCommands = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_unsupported_command_total",
		Help: "Unsupported PG wire commands rejected by the proxy.",
	}, []string{"command"})

	proxyDenylistRejections = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_denylist_rejections_total",
		Help: "Queries rejected by the side-channel denylist.",
	}, []string{"tenant_id", "reason"})

	proxyRLSSyncErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_rls_sync_errors_total",
		Help: "RLS syncer errors.",
	}, []string{"data_source_id"})

	proxyTokenValidations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_token_validations_total",
		Help: "Connection token validation attempts.",
	}, []string{"result"})

	proxyCalciteSidecarErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "proxy_calcite_sidecar_errors_total",
		Help: "Errors returned from the Calcite sidecar.",
	}, []string{"tenant_id"})

	proxyBackendPoolAcquireWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "proxy_pool_acquire_wait_seconds",
		Help:    "Time waiting to acquire a backend connection from the pool.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant_id", "data_source_id"})
)
