package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricDeliveryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_delivery_total",
		Help: "Total webhook delivery attempts.",
	}, []string{"result"}) // result: delivered | failed | dlq

	metricDeliveryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "webhook_delivery_duration_seconds",
		Help:    "Duration of webhook HTTP delivery attempts.",
		Buckets: prometheus.DefBuckets,
	}, []string{"result"})

	metricDLQTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_dlq_total",
		Help: "Total events moved to DLQ.",
	}, []string{"tenant_id"})

	metricKafkaConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "webhook_fanout_kafka_consumed_total",
		Help: "Total Kafka messages consumed by webhook-fanout.",
	}, []string{"topic"})

	metricKafkaLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "webhook_fanout_kafka_lag_seconds",
		Help: "Consumer lag in seconds.",
	}, []string{"topic"})

	metricSSRFBlocked = promauto.NewCounter(prometheus.CounterOpts{
		Name: "webhook_ssrf_blocked_total",
		Help: "SSRF delivery attempts blocked.",
	})

	metricSavedQueryRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saved_query_runs_total",
		Help: "Total saved query scheduler runs.",
	}, []string{"tenant_id", "status"})
)
