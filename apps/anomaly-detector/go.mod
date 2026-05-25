module github.com/governance-platform/anomaly-detector

go 1.22

require (
	github.com/governance-platform/pkg/db v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/jackc/pgx/v5 v5.5.4
	github.com/prometheus/client_golang v1.19.1
	github.com/redis/go-redis/v9 v9.5.1
	github.com/rs/zerolog v1.33.0
	github.com/segmentio/kafka-go v0.4.47
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/attribute v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
	golang.org/x/sync v0.7.0
)

replace (
	github.com/governance-platform/pkg/db          => ../../pkg/db
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/logging      => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry    => ../../pkg/telemetry
)
