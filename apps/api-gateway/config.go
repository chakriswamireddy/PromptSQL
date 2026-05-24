package main

import (
	"os"
	"strconv"
	"strings"
)

type config struct {
	HTTPAddr     string
	Environment  string
	Version      string
	OTLPEndpoint string
	SamplingRate float64
	UnleashURL   string
	UnleashToken string

	// PostgreSQL — used for role resolution, user lookup, refresh tokens.
	DatabaseURL string

	// Redis — JTI replay store, role cache.
	RedisURL string

	// Ed25519 JWT — base64-encoded private key seed (64 bytes) or seed (32 bytes).
	// Load from Vault in staging/prod; set JWT_ED25519_PRIVATE_KEY env var.
	JWTPrivateKeyB64 string
	JWTIssuer        string
	JWTAudience      string

	// HMAC service-to-service secrets — comma-separated "keyID:base64secret" pairs.
	// Example: "v1:c2VjcmV0,v2:bmV3c2VjcmV0"
	HMACSecrets string

	// MFA issuer label shown in authenticator apps.
	TOTPIssuer string

	// PDP gRPC address for admin simulator.
	PDPAddr string
}

func loadConfig() config {
	sr, _ := strconv.ParseFloat(getEnv("OTEL_SAMPLING_RATE", "1.0"), 64)
	return config{
		HTTPAddr:         getEnv("HTTP_ADDR", ":8080"),
		Environment:      getEnv("DEPLOYMENT_ENVIRONMENT", "local"),
		Version:          getEnv("OTEL_SERVICE_VERSION", "local"),
		OTLPEndpoint:     getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SamplingRate:     sr,
		UnleashURL:       getEnv("UNLEASH_URL", "http://localhost:4242/api"),
		UnleashToken:     getEnv("UNLEASH_API_TOKEN", ""),
		DatabaseURL:      getEnv("DATABASE_URL", "postgres://app_login_user:changeme_app@localhost:5432/governance?sslmode=disable"),
		RedisURL:         getEnv("REDIS_URL", "redis://localhost:6379/0"),
		JWTPrivateKeyB64: getEnv("JWT_ED25519_PRIVATE_KEY", ""),
		JWTIssuer:        getEnv("JWT_ISSUER", "https://auth.platform.io"),
		JWTAudience:      getEnv("JWT_AUDIENCE", "platform-api"),
		HMACSecrets:      getEnv("HMAC_SECRETS", ""),
		TOTPIssuer:       getEnv("TOTP_ISSUER", "GovernancePlatform"),
		PDPAddr:          getEnv("PDP_GRPC_ADDR", "localhost:9090"),
	}
}

// parseHMACSecrets decodes the HMAC_SECRETS env var into a keyID→secret map.
func parseHMACSecrets(raw string) map[string][]byte {
	out := make(map[string][]byte)
	if raw == "" {
		return out
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		idx := strings.IndexByte(pair, ':')
		if idx <= 0 {
			continue
		}
		keyID := pair[:idx]
		secret := []byte(pair[idx+1:])
		out[keyID] = secret
	}
	return out
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
