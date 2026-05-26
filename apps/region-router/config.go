package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string
	MetricsAddr     string
	DatabaseURL     string
	UnleashURL      string
	UnleashToken    string
	LocalRegion     string
	AllowedRegions  []string
	UpstreamTimeout time.Duration
	OTELEndpoint    string
	LogLevel        string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		HTTPAddr:        getenv("HTTP_ADDR", ":8080"),
		MetricsAddr:     getenv("METRICS_ADDR", ":9093"),
		DatabaseURL:     mustenv("DATABASE_URL"),
		UnleashURL:      getenv("UNLEASH_URL", "http://unleash.governance-platform:4242/api"),
		UnleashToken:    mustenv("UNLEASH_API_TOKEN"),
		LocalRegion:     mustenv("LOCAL_REGION"),
		AllowedRegions:  strings.Split(getenv("ALLOWED_REGIONS", "us-east-1,eu-west-1"), ","),
		UpstreamTimeout: parseDuration(getenv("UPSTREAM_TIMEOUT", "5s")),
		OTELEndpoint:    getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-collector:4317"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
	}
	if cfg.LocalRegion == "" {
		return nil, fmt.Errorf("LOCAL_REGION must not be empty")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustenv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 5 * time.Second
	}
	return d
}
