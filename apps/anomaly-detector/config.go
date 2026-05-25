package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	HTTPAddr    string
	MetricsAddr string
	Environment string
	Version     string

	OTLPEndpoint string
	SamplingRate float64

	UnleashURL   string
	UnleashToken string

	// PostgreSQL
	DatabaseURL string

	// Redis — hot store for risk scores (TTL 60s).
	RedisURL string

	// Kafka consumer
	KafkaBrokers     []string
	ConsumerGroup    string
	TopicAuditAccess string // audit.access.{env}

	// Kafka producer — scored events
	TopicRiskScored string // risk.scored.{env}

	// Baseline settings
	BaselineWindowDays  int           // rolling window for baseline (default 90)
	WarmupDays          int           // warm-up period before scoring (default 30)
	DecayHalfLifeHours  float64       // exponential decay half-life (default 1.0)
	BaselineFlushPeriod time.Duration // how often baselines are flushed to DB

	// Consumer batch
	BatchSize    int
	BatchTimeout time.Duration

	// Score Redis TTL
	ScoreTTL time.Duration
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	batchSize, _ := strconv.Atoi(getEnv("BATCH_SIZE", "200"))
	batchTimeout, _ := time.ParseDuration(getEnv("BATCH_TIMEOUT", "500ms"))
	baselineWindowDays, _ := strconv.Atoi(getEnv("BASELINE_WINDOW_DAYS", "90"))
	warmupDays, _ := strconv.Atoi(getEnv("WARMUP_DAYS", "30"))
	decayHalfLife, _ := strconv.ParseFloat(getEnv("DECAY_HALF_LIFE_HOURS", "1.0"), 64)
	baselineFlushPeriod, _ := time.ParseDuration(getEnv("BASELINE_FLUSH_PERIOD", "60s"))
	scoreTTL, _ := time.ParseDuration(getEnv("SCORE_TTL", "60s"))

	brokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")

	return config{
		HTTPAddr:    getEnv("HTTP_ADDR", ":8090"),
		MetricsAddr: getEnv("METRICS_ADDR", ":9109"),
		Environment: getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:     getEnv("OTEL_SERVICE_VERSION", "local"),

		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate: sr,

		UnleashURL:   getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken: getEnv("UNLEASH_API_TOKEN", ""),

		DatabaseURL: getEnv("DATABASE_URL", "postgres://app_login_user:password@localhost:5432/governance?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379/0"),

		KafkaBrokers:     brokers,
		ConsumerGroup:    getEnv("KAFKA_CONSUMER_GROUP", "anomaly-detector"),
		TopicAuditAccess: getEnv("KAFKA_TOPIC_AUDIT_ACCESS", "audit.access.local"),
		TopicRiskScored:  getEnv("KAFKA_TOPIC_RISK_SCORED", "risk.scored.local"),

		BaselineWindowDays:  baselineWindowDays,
		WarmupDays:          warmupDays,
		DecayHalfLifeHours:  decayHalfLife,
		BaselineFlushPeriod: baselineFlushPeriod,

		BatchSize:    batchSize,
		BatchTimeout: batchTimeout,
		ScoreTTL:     scoreTTL,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
