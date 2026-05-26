module github.com/org/platform/apps/compliance-service

go 1.22

require (
	github.com/go-chi/chi/v5 v5.0.12
	github.com/jackc/pgx/v5 v5.5.5
	github.com/redis/go-redis/v9 v9.5.1
	github.com/segmentio/kafka-go v0.4.47
	github.com/stripe/stripe-go/v76 v76.25.0
	go.opentelemetry.io/otel v1.26.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.26.0
	go.opentelemetry.io/otel/sdk v1.26.0
	go.opentelemetry.io/otel/trace v1.26.0
	go.uber.org/zap v1.27.0
	github.com/prometheus/client_golang v1.19.0
	github.com/google/uuid v1.6.0
	golang.org/x/crypto v0.22.0
)
