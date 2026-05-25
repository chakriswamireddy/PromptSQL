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
	TopicAccess    string
	TopicPolicy    string
	ConsumerGroup  string
	ListenAddr     string
	MetricsAddr    string
	JWTPublicKey   string // Ed25519 public key PEM for WebSocket auth
	MaxConnPerUser int
	DropBufferSize int
	PingInterval   time.Duration
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	maxConn, _ := strconv.Atoi(getEnv("MAX_CONN_PER_USER", "5"))
	dropBuf, _ := strconv.Atoi(getEnv("DROP_BUFFER_SIZE", "256"))
	ping, _ := time.ParseDuration(getEnv("PING_INTERVAL", "30s"))
	if ping == 0 {
		ping = 30 * time.Second
	}
	return config{
		Environment:    getEnv("ENVIRONMENT", "local"),
		Version:        getEnv("VERSION", "dev"),
		OTLPEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:   sr,
		UnleashURL:     getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:   getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),
		KafkaBrokers:   strings.Split(getEnv("KAFKA_BROKERS", "localhost:9092"), ","),
		TopicAccess:    getEnv("KAFKA_TOPIC_ACCESS", "audit.access.local"),
		TopicPolicy:    getEnv("KAFKA_TOPIC_POLICY", "audit.policy.local"),
		ConsumerGroup:  getEnv("KAFKA_CONSUMER_GROUP", "live-feed-broadcaster"),
		ListenAddr:     getEnv("LISTEN_ADDR", ":8080"),
		MetricsAddr:    getEnv("METRICS_ADDR", ":9105"),
		JWTPublicKey:   getEnv("JWT_PUBLIC_KEY", ""),
		MaxConnPerUser: maxConn,
		DropBufferSize: dropBuf,
		PingInterval:   ping,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
