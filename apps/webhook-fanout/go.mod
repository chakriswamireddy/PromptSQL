module github.com/governance-platform/webhook-fanout

go 1.22

require (
	github.com/governance-platform/pkg/audit v0.0.0
	github.com/governance-platform/pkg/db v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus/client_golang v1.19.1
	github.com/robfig/cron/v3 v3.0.1
	github.com/rs/zerolog v1.33.0
	github.com/segmentio/kafka-go v0.4.47
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
)

replace (
	github.com/governance-platform/pkg/audit => ../../pkg/audit
	github.com/governance-platform/pkg/db => ../../pkg/db
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/logging => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry => ../../pkg/telemetry
)
