package main

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func initOTel(ctx context.Context, endpoint, serviceName string) (func(context.Context) error, error) {
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}
	res, _ := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(serviceName),
	))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

func otelMiddleware(service string) func(http.Handler) http.Handler {
	tracer := otel.Tracer(service)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
			)
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newLogger(level string) *zap.Logger {
	cfg := zap.NewProductionConfig()
	if level == "debug" {
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	l, err := cfg.Build()
	if err != nil {
		panic(err)
	}
	return l
}
