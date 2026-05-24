package main

import (
	"os"
	"strconv"
	"time"
)

type config struct {
	// Listener
	ProxyAddr   string // PG wire protocol listener
	MetricsAddr string // Prometheus + health HTTP

	// Observability
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64

	// Feature flags
	UnleashURL   string
	UnleashToken string

	// Redis — connection token cache (primary store for tokens)
	RedisURL string

	// PDP gRPC
	PDPAddr string

	// Calcite sidecar gRPC
	CalciteAddr string

	// API gateway (for token validation fallback)
	APIGatewayURL string

	// TLS — proxy listens with TLS 1.3
	TLSCertFile string
	TLSKeyFile  string

	// Token TTL (must match api-gateway issuance TTL)
	TokenTTL time.Duration

	// Connection pool limits
	PoolMaxConnsPerTenant int
	PoolMaxConnsStarter   int // Starter plan cap
	PoolMaxConnsPro       int // Pro plan cap
	PoolIdleTimeout       time.Duration

	// Query limits
	DefaultMaxRows      int64
	DefaultMaxCost      float64
	ExplainCacheTTL     time.Duration
	RewriteCacheTTL     time.Duration
	StatementTimeout    time.Duration

	// Result set cap
	ResultSetRowCap int64
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)

	poolMax, _ := strconv.Atoi(getEnv("POOL_MAX_CONNS_PER_TENANT", "100"))
	poolStarter, _ := strconv.Atoi(getEnv("POOL_MAX_CONNS_STARTER", "25"))
	poolPro, _ := strconv.Atoi(getEnv("POOL_MAX_CONNS_PRO", "100"))
	defaultMaxRows, _ := strconv.ParseInt(getEnv("DEFAULT_MAX_ROWS", "1000000"), 10, 64)
	defaultMaxCost, _ := strconv.ParseFloat(getEnv("DEFAULT_MAX_COST", "10000"), 64)
	resultSetCap, _ := strconv.ParseInt(getEnv("RESULT_SET_ROW_CAP", "100000"), 10, 64)

	return config{
		ProxyAddr:             getEnv("PROXY_ADDR", ":5433"),
		MetricsAddr:           getEnv("METRICS_ADDR", ":9094"),
		Environment:           getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:               getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint:          getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:          sr,
		UnleashURL:            getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:          getEnv("UNLEASH_API_TOKEN", ""),
		RedisURL:              getEnv("REDIS_URL", "redis://localhost:6379/0"),
		PDPAddr:               getEnv("PDP_GRPC_ADDR", "localhost:9090"),
		CalciteAddr:           getEnv("CALCITE_GRPC_ADDR", "localhost:9095"),
		APIGatewayURL:         getEnv("API_GATEWAY_URL", "http://localhost:8080"),
		TLSCertFile:           getEnv("TLS_CERT_FILE", ""),
		TLSKeyFile:            getEnv("TLS_KEY_FILE", ""),
		TokenTTL:              15 * time.Minute,
		PoolMaxConnsPerTenant: poolMax,
		PoolMaxConnsStarter:   poolStarter,
		PoolMaxConnsPro:       poolPro,
		PoolIdleTimeout:       5 * time.Minute,
		DefaultMaxRows:        defaultMaxRows,
		DefaultMaxCost:        defaultMaxCost,
		ExplainCacheTTL:       60 * time.Second,
		RewriteCacheTTL:       5 * time.Minute,
		StatementTimeout:      30 * time.Second,
		ResultSetRowCap:       resultSetCap,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
