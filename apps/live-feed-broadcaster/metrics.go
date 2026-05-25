package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricActiveConns = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "wsfeed_active_connections",
		Help: "Number of active WebSocket connections.",
	}, []string{"tenant_id"})

	metricDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wsfeed_dropped_total",
		Help: "Total events dropped due to slow consumer backpressure.",
	}, []string{"tenant_id"})

	metricBroadcast = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wsfeed_broadcast_total",
		Help: "Total events broadcast to WebSocket clients.",
	}, []string{"tenant_id", "event_type"})

	metricKafkaConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wsfeed_kafka_consumed_total",
		Help: "Total Kafka messages consumed.",
	}, []string{"topic"})

	metricKafkaLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kafka_consumer_lag_seconds",
		Help: "Consumer lag in seconds for live-feed topics.",
	}, []string{"topic", "consumer"})

	metricAuthErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "wsfeed_auth_errors_total",
		Help: "WebSocket upgrade auth failures.",
	})
)
