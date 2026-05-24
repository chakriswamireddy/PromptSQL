package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Environment    string
	Version        string
	OTLPEndpoint   string
	SamplingRate   float64
	UnleashURL     string
	UnleashToken   string
	KafkaBrokers   []string
	TopicPolicy    string
	TopicAccess    string
	TopicSystem    string
	ConsumerGroup  string
	ClickHouseDSN  string
	MetricsAddr    string
	DLQTopicSuffix string
	BatchSize      int
	BatchTimeout   time.Duration
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	bs, _ := strconv.Atoi(getEnv("BATCH_SIZE", "1000"))
	bt, _ := time.ParseDuration(getEnv("BATCH_TIMEOUT", "1s"))
	if bt == 0 {
		bt = time.Second
	}
	brokers := strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	return config{
		Environment:    getEnv("ENVIRONMENT", "local"),
		Version:        getEnv("VERSION", "dev"),
		OTLPEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:   sr,
		UnleashURL:     getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:   getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),
		KafkaBrokers:   brokers,
		TopicPolicy:    getEnv("KAFKA_TOPIC_POLICY", "audit.policy.local"),
		TopicAccess:    getEnv("KAFKA_TOPIC_ACCESS", "audit.access.local"),
		TopicSystem:    getEnv("KAFKA_TOPIC_SYSTEM", "audit.system.local"),
		ConsumerGroup:  getEnv("KAFKA_CONSUMER_GROUP", "audit-clickhouse-sink"),
		ClickHouseDSN:  getEnv("CLICKHOUSE_DSN", "clickhouse://localhost:9000/default?username=default&password="),
		MetricsAddr:    getEnv("METRICS_ADDR", ":9102"),
		DLQTopicSuffix: getEnv("DLQ_TOPIC_SUFFIX", ".dlq.clickhouse"),
		BatchSize:      bs,
		BatchTimeout:   bt,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
