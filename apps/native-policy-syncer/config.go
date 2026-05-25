package main

import (
	"os"
	"strconv"
	"time"
)

// config holds all runtime configuration for native-policy-syncer.
// Values are sourced from environment variables; defaults are safe for local dev.
// Secrets (DSNs, API keys) are intentionally absent — they are resolved at
// runtime from Vault via DataSource.SecretRef.
type config struct {
	HTTPAddr     string
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64
	UnleashURL   string
	UnleashToken string

	// DatabaseURL is the control-plane PostgreSQL DSN.
	// Must not be a target-database DSN — target DSNs come from Vault per data_source.
	DatabaseURL string

	// SyncInterval is how often the background sync loop runs.
	SyncInterval time.Duration

	// SyncConcurrency is the maximum number of data sources synced in parallel.
	SyncConcurrency int

	// VaultAddr is the Vault server address used to resolve DataSource.SecretRef.
	VaultAddr string

	// VaultRole is the service's Vault AppRole / K8s auth role.
	VaultRole string

	// SyncTimeoutPerSource is the maximum time allowed for a single data-source sync.
	SyncTimeoutPerSource time.Duration

	// PerEngineFlags maps engine names to their per-engine feature flag names.
	// Checked at handler level before attempting connector creation.
	// Built from static constants — not from env.
	PerEngineFlags map[string]string
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	syncInterval, _ := time.ParseDuration(getEnv("SYNC_INTERVAL", "1h"))
	syncConc, _ := strconv.Atoi(getEnv("SYNC_CONCURRENCY", "4"))
	syncTimeout, _ := time.ParseDuration(getEnv("SYNC_TIMEOUT_PER_SOURCE", "5m"))

	return config{
		HTTPAddr:     getEnv("HTTP_ADDR", ":8085"),
		Environment:  getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:      getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate: sr,
		UnleashURL:   getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken: getEnv("UNLEASH_API_TOKEN", ""),
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://app_write:app_write@localhost:5432/governance?sslmode=disable"),
		VaultAddr:    getEnv("VAULT_ADDR", "http://localhost:8200"),
		VaultRole:    getEnv("VAULT_ROLE", "native-policy-syncer"),

		SyncInterval:        syncInterval,
		SyncConcurrency:     syncConc,
		SyncTimeoutPerSource: syncTimeout,

		// Per-engine feature flags (static; not from env).
		PerEngineFlags: map[string]string{
			"mysql":      "multi-db-mysql",
			"sqlserver":  "multi-db-sqlserver",
			"oracle":     "multi-db-oracle",
			"snowflake":  "multi-db-snowflake",
			"bigquery":   "multi-db-bigquery",
			"databricks": "multi-db-databricks",
			"mongodb":    "multi-db-mongodb",
			"postgres":   "multi-db", // postgres gated by the parent flag
		},
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
