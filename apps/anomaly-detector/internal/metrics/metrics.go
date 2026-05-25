// Package metrics registers all Prometheus instruments for the anomaly detector.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	EventsConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "anomaly_detector_events_consumed_total",
		Help: "Total access events consumed from Kafka.",
	}, []string{"tenant_id"})

	EventsScored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "anomaly_detector_events_scored_total",
		Help: "Total events that produced a risk score.",
	}, []string{"tenant_id"})

	EventsSkippedWarmup = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "anomaly_detector_events_skipped_warmup_total",
		Help: "Events skipped because the user is still in warm-up.",
	}, []string{"tenant_id"})

	EventsSkippedAllowlist = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "anomaly_detector_events_skipped_allowlist_total",
		Help: "Events skipped because the principal is allowlisted.",
	}, []string{"tenant_id"})

	ScoreHigh = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "anomaly_detector_score_high_total",
		Help: "Events where final score >= 70 (elevated risk).",
	}, []string{"tenant_id"})

	ScoringDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "anomaly_detector_scoring_duration_seconds",
		Help:    "Time to score a single access event including Redis write.",
		Buckets: prometheus.DefBuckets,
	})

	KafkaLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "anomaly_detector_kafka_lag_seconds",
		Help: "Consumer lag in seconds (event_time to now delta).",
	}, []string{"topic"})

	BaselineFlushDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "anomaly_detector_baseline_flush_duration_seconds",
		Help:    "Time to flush all dirty baselines to PostgreSQL.",
		Buckets: prometheus.DefBuckets,
	})

	BaselineFlushErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "anomaly_detector_baseline_flush_errors_total",
		Help: "Total baseline flush errors.",
	})

	ActiveUsers = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "anomaly_detector_active_users",
		Help: "Number of users with an in-memory baseline.",
	}, []string{"tenant_id"})

	RedisWriteErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "anomaly_detector_redis_write_errors_total",
		Help: "Total Redis write errors when persisting risk scores.",
	})

	KafkaPublishErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "anomaly_detector_kafka_publish_errors_total",
		Help: "Total errors publishing to risk.scored topic.",
	})
)
