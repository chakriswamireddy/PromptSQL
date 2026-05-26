package main

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port            string
	DatabaseURL     string
	RedisAddr       string
	KafkaBrokers    string
	OTelEndpoint    string
	UnleashURL      string
	UnleashToken    string
	StripeSecretKey string
	StripeWebhookSecret string
	VaultAddr       string
	LogLevel        string
	HealthScoreCron string
	EvidenceCron    string
	AccessReviewCron string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		Port:             getEnv("PORT", "8096"),
		DatabaseURL:      mustEnv("DATABASE_URL"),
		RedisAddr:        getEnv("REDIS_ADDR", "localhost:6379"),
		KafkaBrokers:     getEnv("KAFKA_BROKERS", "localhost:9092"),
		OTelEndpoint:     getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		UnleashURL:       getEnv("UNLEASH_URL", "http://unleash:4242"),
		UnleashToken:     getEnv("UNLEASH_TOKEN", ""),
		StripeSecretKey:  getEnv("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret: getEnv("STRIPE_WEBHOOK_SECRET", ""),
		VaultAddr:        getEnv("VAULT_ADDR", "http://vault:8200"),
		LogLevel:         getEnv("LOG_LEVEL", "info"),
		HealthScoreCron:  getEnv("HEALTH_SCORE_CRON", "0 2 * * *"),
		EvidenceCron:     getEnv("EVIDENCE_CRON", "0 3 * * *"),
		AccessReviewCron: getEnv("ACCESS_REVIEW_CRON", "0 4 1 1,4,7,10 *"),
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
