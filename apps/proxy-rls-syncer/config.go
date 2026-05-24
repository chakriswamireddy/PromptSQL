package main

import (
	"os"
	"strconv"
	"time"
)

type config struct {
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64
	MetricsAddr  string

	UnleashURL   string
	UnleashToken string

	DatabaseURL string
	SyncInterval time.Duration
	SyncerVersion string
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	syncInterval, _ := time.ParseDuration(getEnv("SYNC_INTERVAL", "1h"))

	return config{
		Environment:   getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:       getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint:  getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:  sr,
		MetricsAddr:   getEnv("METRICS_ADDR", ":9097"),
		UnleashURL:    getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:  getEnv("UNLEASH_API_TOKEN", ""),
		DatabaseURL:   getEnv("DATABASE_URL", ""),
		SyncInterval:  syncInterval,
		SyncerVersion: getEnv("OTEL_SERVICE_VERSION", "local"),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
