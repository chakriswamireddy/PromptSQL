package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config error", "error", err)
		os.Exit(1)
	}

	// Feature flag check: if scale-multiregion is off, exit cleanly.
	// In practice the Unleash SDK would be initialized here; using env var for brevity.
	if os.Getenv("FEATURE_SCALE_MULTIREGION") == "false" {
		log.Info("scale-multiregion feature flag is off; region-router exiting cleanly")
		os.Exit(0)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// ── OTel setup ────────────────────────────────────────────────────────────
	tp, err := initTracer(ctx, cfg)
	if err != nil {
		log.Error("otel init failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := otel.Tracer("region-router")

	// ── Database ──────────────────────────────────────────────────────────────
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		log.Error("db open failed", "error", err)
		os.Exit(1)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Error("db ping failed", "error", err)
		os.Exit(1)
	}

	store := newResidencyStore(db, tracer)

	// ── HTTP router ───────────────────────────────────────────────────────────
	handler, err := newRouterHandler(cfg, store, tracer, log)
	if err != nil {
		log.Error("handler init failed", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", readyHandler(store))
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	metricsSrv := &http.Server{
		Addr:    cfg.MetricsAddr,
		Handler: promhttp.Handler(),
	}

	go func() {
		log.Info("region-router listening", "addr", cfg.HTTPAddr, "region", cfg.LocalRegion)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server error", "error", err)
			cancel()
		}
	}()

	go func() {
		if err := metricsSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server error", "error", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down region-router")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
}

func initTracer(ctx context.Context, cfg *Config) (*sdktrace.TracerProvider, error) {
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTELEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otel exporter: %w", err)
	}

	res, _ := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("region-router"),
			semconv.DeploymentEnvironment(os.Getenv("DEPLOYMENT_ENVIRONMENT")),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}
