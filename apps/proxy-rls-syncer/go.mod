module github.com/governance-platform/proxy-rls-syncer

go 1.22

require (
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/db v0.0.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus/client_golang v1.19.1
)

replace (
	github.com/governance-platform/pkg/logging => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry => ../../pkg/telemetry
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/db => ../../pkg/db
)
