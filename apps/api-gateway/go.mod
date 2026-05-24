module github.com/governance-platform/api-gateway

go 1.22

require (
	github.com/governance-platform/pkg/auth v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/governance-platform/pkg/grpc v0.0.0
	github.com/governance-platform/pkg/pdpv1 v0.0.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/pquerna/otp v1.4.0
	github.com/redis/go-redis/v9 v9.5.1
	github.com/rs/zerolog v1.33.0
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
	golang.org/x/crypto v0.24.0
	google.golang.org/grpc v1.64.0
)

require (
	github.com/boombuler/barcode v1.0.1 // indirect
	github.com/bsm/redislock v0.9.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/golang-jwt/jwt/v5 v5.2.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.27.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.49.0 // indirect
	go.opentelemetry.io/otel/sdk v1.27.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.27.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.21.0 // indirect
	golang.org/x/text v0.16.0 // indirect
)

replace (
	github.com/governance-platform/pkg/auth => ../../pkg/auth
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/logging => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry => ../../pkg/telemetry
	github.com/governance-platform/pkg/grpc => ../../pkg/grpc
	github.com/governance-platform/pkg/pdpv1 => ../../pkg/pdpv1
)
