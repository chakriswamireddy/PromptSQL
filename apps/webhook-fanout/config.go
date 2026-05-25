package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64
	UnleashURL   string
	UnleashToken string

	KafkaBrokers  []string
	TopicAccess   string
	TopicPolicy   string
	TopicSystem   string
	ConsumerGroup string

	DatabaseDSN string

	VaultAddr  string
	VaultToken string

	MetricsAddr string

	// Delivery tuning.
	DeliveryTimeout    time.Duration
	MaxRetries         int
	RetrySchedule      []time.Duration // [1m, 5m, 30m, 2h, 12h]
	MaxPayloadBytes    int
	MaxHeaderBytes     int
	CircuitBreakerRate float64 // fraction of failures to open circuit (0.5 = 50%)
	CircuitBreakerWin  time.Duration

	// Scheduler.
	SchedulerInterval time.Duration
	SchedulerJitter   time.Duration
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	maxRetries, _ := strconv.Atoi(getEnv("MAX_RETRIES", "5"))
	deliveryTimeout, _ := time.ParseDuration(getEnv("DELIVERY_TIMEOUT", "30s"))
	if deliveryTimeout == 0 {
		deliveryTimeout = 30 * time.Second
	}
	maxPayload, _ := strconv.Atoi(getEnv("MAX_PAYLOAD_BYTES", "65536"))
	if maxPayload == 0 {
		maxPayload = 64 * 1024
	}
	maxHeader, _ := strconv.Atoi(getEnv("MAX_HEADER_BYTES", "16384"))
	if maxHeader == 0 {
		maxHeader = 16 * 1024
	}
	cbRate, _ := strconv.ParseFloat(getEnv("CIRCUIT_BREAKER_RATE", "0.5"), 64)
	cbWin, _ := time.ParseDuration(getEnv("CIRCUIT_BREAKER_WINDOW", "1m"))
	if cbWin == 0 {
		cbWin = time.Minute
	}
	schedInterval, _ := time.ParseDuration(getEnv("SCHEDULER_INTERVAL", "30s"))
	if schedInterval == 0 {
		schedInterval = 30 * time.Second
	}
	schedJitter, _ := time.ParseDuration(getEnv("SCHEDULER_JITTER", "5s"))
	brokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")

	return config{
		Environment:  getEnv("ENVIRONMENT", "local"),
		Version:      getEnv("VERSION", "dev"),
		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate: sr,
		UnleashURL:   getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken: getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),

		KafkaBrokers:  brokers,
		TopicAccess:   getEnv("KAFKA_TOPIC_ACCESS", "audit.access.local"),
		TopicPolicy:   getEnv("KAFKA_TOPIC_POLICY", "audit.policy.local"),
		TopicSystem:   getEnv("KAFKA_TOPIC_SYSTEM", "audit.system.local"),
		ConsumerGroup: getEnv("KAFKA_CONSUMER_GROUP", "webhook-fanout"),

		DatabaseDSN: getEnv("DATABASE_DSN", "postgres://localhost:5432/governance?sslmode=disable"),

		VaultAddr:  getEnv("VAULT_ADDR", "http://localhost:8200"),
		VaultToken: getEnv("VAULT_TOKEN", ""),

		MetricsAddr: getEnv("METRICS_ADDR", ":9106"),

		DeliveryTimeout: deliveryTimeout,
		MaxRetries:      maxRetries,
		RetrySchedule: []time.Duration{
			time.Minute,
			5 * time.Minute,
			30 * time.Minute,
			2 * time.Hour,
			12 * time.Hour,
		},
		MaxPayloadBytes:    maxPayload,
		MaxHeaderBytes:     maxHeader,
		CircuitBreakerRate: cbRate,
		CircuitBreakerWin:  cbWin,

		SchedulerInterval: schedInterval,
		SchedulerJitter:   schedJitter,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
