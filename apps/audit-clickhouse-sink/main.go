package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"

	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
)

const (
	serviceName = "audit-clickhouse-sink"
	featureFlag = "audit-pipeline"
)

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel — must be first.
	tel, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:    serviceName,
		ServiceVersion: cfg.Version,
		Environment:    cfg.Environment,
		OTLPEndpoint:   cfg.OTLPEndpoint,
		SamplingRate:   cfg.SamplingRate,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init telemetry")
	}
	defer tel.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tel.TracerProvider())
	otel.SetMeterProvider(tel.MeterProvider())

	// 2. Feature flag gate.
	ff, err := featureflags.New(ctx, featureflags.Config{
		UnleashURL:  cfg.UnleashURL,
		APIToken:    cfg.UnleashToken,
		AppName:     serviceName,
		Environment: cfg.Environment,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init feature flags")
	}
	defer ff.Close()

	if !ff.IsEnabled(featureFlag) {
		log.Info().Str("flag", featureFlag).Msg("feature flag disabled — exiting cleanly")
		os.Exit(0)
	}

	// 3. ClickHouse connection.
	ch, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{cfg.ClickHouseDSN},
		Settings: clickhouse.Settings{
			"async_insert":          1,
			"wait_for_async_insert": 0,
		},
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open ClickHouse")
	}
	defer ch.Close() //nolint:errcheck

	if err := ch.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("ClickHouse ping failed")
	}

	// 4. Start consumer.
	consumer := newConsumer(cfg, ch, log)
	consumer.Run(ctx)
	defer consumer.Close()

	// 5. HTTP health + metrics.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := ch.Ping(r.Context()); err != nil {
			http.Error(w, "clickhouse unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())

	httpSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics/health listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("http server error")
		}
	}()

	// 6. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	log.Info().Msg("audit-clickhouse-sink stopped")
}
