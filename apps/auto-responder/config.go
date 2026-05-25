package main

import (
	"os"
	"strconv"
	"strings"
)

type config struct {
	// Server
	Addr        string
	MetricsAddr string
	Version     string
	Environment string

	// Dependencies
	DatabaseURL    string
	RedisURL       string
	KafkaBrokers   []string
	UnleashURL     string
	UnleashToken   string
	OTLPEndpoint   string
	SamplingRate   float64

	// Auth
	ObligationKeyB64   string // base64-encoded HMAC key for obligation tokens
	JWTPublicKeyB64    string // Ed25519 public key for verifying caller JWTs
	HMACSecrets        string // HMAC secrets for internal service-to-service auth

	// Break-glass
	MaxBreakGlassDurationSec int // per-tenant maximum; hard cap
}

func loadConfig() config {
	return config{
		Addr:                     getEnvDefault("ADDR", ":8095"),
		MetricsAddr:              getEnvDefault("METRICS_ADDR", ":9095"),
		Version:                  getEnvDefault("VERSION", "dev"),
		Environment:              getEnvDefault("ENVIRONMENT", "development"),
		DatabaseURL:              mustEnv("DATABASE_URL"),
		RedisURL:                 getEnvDefault("REDIS_URL", "redis://localhost:6379"),
		KafkaBrokers:             splitCSV(getEnvDefault("KAFKA_BROKERS", "localhost:9092")),
		UnleashURL:               getEnvDefault("UNLEASH_URL", "http://unleash:4242"),
		UnleashToken:             getEnvDefault("UNLEASH_TOKEN", "default:development.unleash-insecure-api-token"),
		OTLPEndpoint:             getEnvDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4318"),
		SamplingRate:             parseFloat(getEnvDefault("OTEL_SAMPLING_RATE", "1.0")),
		ObligationKeyB64:         getEnvDefault("OBLIGATION_HMAC_KEY_B64", ""),
		JWTPublicKeyB64:          getEnvDefault("JWT_ED25519_PUBLIC_KEY", ""),
		HMACSecrets:              getEnvDefault("HMAC_SECRETS", ""),
		MaxBreakGlassDurationSec: parseInt(getEnvDefault("MAX_BREAK_GLASS_DURATION_SEC", "3600")),
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic("required env var not set: " + k)
	}
	return v
}

func getEnvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
