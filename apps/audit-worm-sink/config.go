package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Environment      string
	Version          string
	OTLPEndpoint     string
	SamplingRate     float64
	UnleashURL       string
	UnleashToken     string
	KafkaBrokers     []string
	TopicPolicy      string
	TopicAccess      string
	TopicSystem      string
	ConsumerGroup    string
	S3Endpoint       string // MinIO in dev, empty = AWS S3 in prod
	S3Bucket         string
	S3Region         string
	S3ForcePathStyle bool
	RetentionYears   int
	MetricsAddr      string
	FlushInterval    time.Duration
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	ry, _ := strconv.Atoi(getEnv("WORM_RETENTION_YEARS", "7"))
	fps, _ := strconv.ParseBool(getEnv("S3_FORCE_PATH_STYLE", "true"))
	fi, _ := time.ParseDuration(getEnv("FLUSH_INTERVAL", "1h"))
	if fi == 0 {
		fi = time.Hour
	}
	brokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	return config{
		Environment:      getEnv("ENVIRONMENT", "local"),
		Version:          getEnv("VERSION", "dev"),
		OTLPEndpoint:     getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:     sr,
		UnleashURL:       getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:     getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),
		KafkaBrokers:     brokers,
		TopicPolicy:      getEnv("KAFKA_TOPIC_POLICY", "audit.policy.local"),
		TopicAccess:      getEnv("KAFKA_TOPIC_ACCESS", "audit.access.local"),
		TopicSystem:      getEnv("KAFKA_TOPIC_SYSTEM", "audit.system.local"),
		ConsumerGroup:    getEnv("KAFKA_CONSUMER_GROUP", "audit-worm-sink"),
		S3Endpoint:       getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3Bucket:         getEnv("S3_BUCKET", "audit-worm-dev"),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3ForcePathStyle: fps,
		RetentionYears:   ry,
		MetricsAddr:      getEnv("METRICS_ADDR", ":9103"),
		FlushInterval:    fi,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
