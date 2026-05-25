package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// syncMetrics holds all Prometheus metrics for the native-policy-syncer.
type syncMetrics struct {
	// syncDuration is a histogram of per-engine sync duration in seconds.
	syncDuration *prometheus.HistogramVec

	// syncErrors is a counter of sync errors by engine.
	syncErrors *prometheus.CounterVec

	// policiesSynced is a counter of policies successfully applied by engine.
	policiesSynced *prometheus.CounterVec

	// syncSkipped is a counter of skipped syncs (version unchanged) by engine.
	syncSkipped *prometheus.CounterVec

	// connectorUp is a gauge (1=up, 0=down) per engine.
	connectorUp *prometheus.GaugeVec

	// policyDrift tracks the number of data sources where the applied
	// policy version differs from the current policy_set_version.
	policyDrift *prometheus.GaugeVec

	// lastSyncAge is a gauge of seconds since last successful sync, per engine.
	lastSyncAge *prometheus.GaugeVec
}

// newMetrics registers and returns all Prometheus metrics.
// promauto panics on duplicate registration, which surfaces immediately in tests.
func newMetrics() *syncMetrics {
	return &syncMetrics{
		syncDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "multi_db_sync_duration_seconds",
			Help:    "Duration of a native policy sync operation per engine.",
			Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300},
		}, []string{"engine", "status"}),

		syncErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "multi_db_sync_errors_total",
			Help: "Total number of errors encountered during native policy sync.",
		}, []string{"engine", "error_kind"}),

		policiesSynced: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "multi_db_policies_synced_total",
			Help: "Total number of policies successfully synced to engine-native constructs.",
		}, []string{"engine"}),

		syncSkipped: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "multi_db_sync_skipped_total",
			Help: "Total number of syncs skipped because the policy version has not changed.",
		}, []string{"engine"}),

		connectorUp: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "multi_db_connector_up",
			Help: "Whether the engine connector is reachable (1=up, 0=down).",
		}, []string{"engine", "data_source_id"}),

		policyDrift: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "multi_db_policy_drift_count",
			Help: "Number of data sources where the applied policy version differs from the current version.",
		}, []string{"engine"}),

		lastSyncAge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "multi_db_last_sync_age_seconds",
			Help: "Seconds since the last successful sync for a given engine.",
		}, []string{"engine", "data_source_id"}),
	}
}
