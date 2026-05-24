package main

import (
	"os"
	"strconv"
)

type config struct {
	GRPCAddr        string
	HTTPAddr        string
	MetricsAddr     string
	Environment     string
	Version         string
	OTLPEndpoint    string
	SamplingRate    float64
	UnleashURL      string
	UnleashToken    string

	// PostgreSQL DSN for the control-plane database.
	DatabaseURL string
	// Redis URL for L2 cache and pub/sub.
	RedisURL string
	// HMAC secrets for SessionContext verification.
	// Format: "keyID1:base64secret1,keyID2:base64secret2"
	HMACSecrets string
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	return config{
		GRPCAddr:     getEnv("GRPC_ADDR", ":9092"),
		HTTPAddr:     getEnv("HTTP_ADDR", ":8080"),
		MetricsAddr:  getEnv("METRICS_ADDR", ":9093"),
		Environment:  getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:      getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate: sr,
		UnleashURL:   getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken: getEnv("UNLEASH_API_TOKEN", ""),
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://app_login_user:password@localhost:5432/governance?sslmode=disable"),
		RedisURL:     getEnv("REDIS_URL", "redis://localhost:6379/0"),
		HMACSecrets:  getEnv("HMAC_SECRETS", ""),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
