package main

import (
	"os"
	"strconv"
)

type config struct {
	Environment      string
	Version          string
	OTLPEndpoint     string
	SamplingRate     float64
	UnleashURL       string
	UnleashToken     string
	DatabaseURL      string
	S3Endpoint       string
	S3Bucket         string
	S3Region         string
	S3ForcePathStyle bool
	MetricsAddr      string
	// DailySampleRate is the fraction of tenants sampled in the daily pass (0..1).
	DailySampleRate float64
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	fps, _ := strconv.ParseBool(getEnv("S3_FORCE_PATH_STYLE", "true"))
	dsr, _ := strconv.ParseFloat(getEnv("DAILY_SAMPLE_RATE", "0.10"), 64)
	return config{
		Environment:      getEnv("ENVIRONMENT", "local"),
		Version:          getEnv("VERSION", "dev"),
		OTLPEndpoint:     getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:     sr,
		UnleashURL:       getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:     getEnv("UNLEASH_API_TOKEN", "default:development.unleash-insecure-api-token"),
		DatabaseURL:      getEnv("DATABASE_URL", "postgres://app:changeme@localhost:5432/governance"),
		S3Endpoint:       getEnv("S3_ENDPOINT", "http://localhost:9000"),
		S3Bucket:         getEnv("S3_BUCKET", "audit-worm-dev"),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3ForcePathStyle: fps,
		MetricsAddr:      getEnv("METRICS_ADDR", ":9104"),
		DailySampleRate:  dsr,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
