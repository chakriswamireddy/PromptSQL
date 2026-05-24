package main

import (
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	RedisURL    string
	PDPAddr     string

	UnleashURL   string
	UnleashToken string

	OTLPEndpoint string
	SamplingRate float64
	Version      string
	Environment  string

	// Kafka / audit
	KafkaBrokers    string
	AuditHMACKey    string
	KafkaTopicSystem string

	// Embedding
	OpenAIAPIKey  string
	EmbeddingModel string
	EmbeddingDims  int

	// Retrieval behaviour
	SnapshotTTLSec  int // Redis TTL for snapshots (default 300)
	DocResultTTLSec int // Redis TTL for doc results (default 60)
	EmbedTTLSec     int // Redis TTL for query embeddings (default 3600)
	MaxChunkBytes   int // Truncation threshold (default 4096)
	QuarantineSweepInterval string // e.g. "1h"
}

func loadConfig() Config {
	return Config{
		HTTPAddr:    getEnv("HTTP_ADDR", ":8083"),
		DatabaseURL: mustEnv("DATABASE_URL"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379/0"),
		PDPAddr:     getEnv("PDP_ADDR", "pdp:9090"),

		UnleashURL:   getEnv("UNLEASH_URL", "http://unleash:4242/api"),
		UnleashToken: getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),

		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317"),
		SamplingRate: parseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0")),
		Version:      getEnv("SERVICE_VERSION", "dev"),
		Environment:  getEnv("DEPLOYMENT_ENVIRONMENT", "development"),

		KafkaBrokers:     getEnv("KAFKA_BROKERS", ""),
		AuditHMACKey:     getEnv("AUDIT_HMAC_KEY", "dev-audit-hmac-key-32-bytes!!!!!"),
		KafkaTopicSystem: getEnv("KAFKA_TOPIC_SYSTEM", "audit.system"),

		OpenAIAPIKey:  getEnv("OPENAI_API_KEY", ""),
		EmbeddingModel: getEnv("EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingDims:  parseInt(getEnv("EMBEDDING_DIMS", "1536")),

		SnapshotTTLSec:  parseInt(getEnv("SNAPSHOT_TTL_SEC", "300")),
		DocResultTTLSec: parseInt(getEnv("DOC_RESULT_TTL_SEC", "60")),
		EmbedTTLSec:     parseInt(getEnv("EMBED_TTL_SEC", "3600")),
		MaxChunkBytes:   parseInt(getEnv("MAX_CHUNK_BYTES", "4096")),
		QuarantineSweepInterval: getEnv("QUARANTINE_SWEEP_INTERVAL", "1h"),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("required environment variable not set: " + key)
	}
	return v
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseInt(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
