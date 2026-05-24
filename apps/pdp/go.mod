module github.com/governance-platform/pdp

go 1.22

require (
	github.com/governance-platform/pkg/auth v0.0.0
	github.com/governance-platform/pkg/db v0.0.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/grpc v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/governance-platform/pkg/pdpv1 v0.0.0
	github.com/governance-platform/policy-engine v0.0.0
	github.com/jackc/pgx/v5 v5.6.0
	github.com/prometheus/client_golang v1.19.1
	github.com/redis/go-redis/v9 v9.5.3
	github.com/rs/zerolog v1.33.0
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
	golang.org/x/sync v0.7.0
	google.golang.org/grpc v1.64.0
	google.golang.org/protobuf v1.34.2
)

replace (
	github.com/governance-platform/pkg/auth       => ../../pkg/auth
	github.com/governance-platform/pkg/db         => ../../pkg/db
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/grpc       => ../../pkg/grpc
	github.com/governance-platform/pkg/logging    => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry  => ../../pkg/telemetry
	github.com/governance-platform/pkg/pdpv1      => ../../pkg/pdpv1
	github.com/governance-platform/policy-engine  => ../../packages/policy-engine
)
