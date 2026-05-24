module github.com/governance-platform/audit-worm-sink

go 1.22

require (
	github.com/aws/aws-sdk-go-v2 v1.30.0
	github.com/aws/aws-sdk-go-v2/config v1.27.24
	github.com/aws/aws-sdk-go-v2/service/s3 v1.58.0
	github.com/governance-platform/pkg/featureflags v0.0.0
	github.com/governance-platform/pkg/logging v0.0.0
	github.com/governance-platform/pkg/telemetry v0.0.0
	github.com/klauspost/compress v1.17.9
	github.com/prometheus/client_golang v1.19.1
	github.com/rs/zerolog v1.33.0
	github.com/segmentio/kafka-go v0.4.47
	go.opentelemetry.io/otel v1.27.0
	go.opentelemetry.io/otel/trace v1.27.0
)

replace (
	github.com/governance-platform/pkg/featureflags => ../../pkg/featureflags
	github.com/governance-platform/pkg/logging      => ../../pkg/logging
	github.com/governance-platform/pkg/telemetry    => ../../pkg/telemetry
)
