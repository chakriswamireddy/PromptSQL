module github.com/governance-platform/native-policy-syncer

go 1.22

require (
	github.com/governance-platform/pkg/audit v0.0.0
	github.com/governance-platform/pkg/connectors v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus/client_golang v1.19.1
	github.com/rs/zerolog v1.33.0
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
)

replace (
	github.com/governance-platform/pkg/audit => ../../pkg/audit
	github.com/governance-platform/pkg/connectors => ../../pkg/connectors
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/logging => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry => ../../pkg/telemetry
)
