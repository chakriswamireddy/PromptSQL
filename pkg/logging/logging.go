// Package logging provides a structured logger (zerolog) pre-configured with
// W3C trace-context injection and a canonical log schema.
package logging

import (
	"context"
	"os"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

// Fields that every service log line must carry.
const (
	FieldService     = "service"
	FieldVersion     = "version"
	FieldEnvironment = "env"
	FieldTenantID    = "tenant_id"
	FieldTraceID     = "trace_id"
	FieldSpanID      = "span_id"
)

// New creates a zerolog.Logger bound to the given service metadata.
// In production (env != "local") logs are emitted as JSON; locally, pretty.
func New(service, version, env string) zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano

	var base zerolog.Logger
	if env == "local" {
		base = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			With().Timestamp().Logger()
	} else {
		base = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}

	return base.With().
		Str(FieldService, service).
		Str(FieldVersion, version).
		Str(FieldEnvironment, env).
		Logger()
}

// WithContext returns a child logger enriched with the trace/span IDs from
// the given context. Call this at every handler entry point.
func WithContext(ctx context.Context, log zerolog.Logger) zerolog.Logger {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return log
	}
	return log.With().
		Str(FieldTraceID, span.SpanContext().TraceID().String()).
		Str(FieldSpanID, span.SpanContext().SpanID().String()).
		Logger()
}
