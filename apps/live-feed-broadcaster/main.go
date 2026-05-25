package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"

	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
)

const (
	serviceName = "live-feed-broadcaster"
	featureFlag = "realtime-stream"
)

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel — first.
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

	// 3. Hub + Kafka consumer.
	hub := newHub(cfg.DropBufferSize)
	go hub.Run()

	consumer := newKafkaConsumer(hub, cfg, log)
	consumer.Run(ctx)
	defer consumer.Close()

	// 4. HTTP server: WebSocket + health + metrics.
	wsHandler := newWSHandler(hub, cfg, log)

	mux := http.NewServeMux()
	mux.Handle("/v1/live-feed", wsHandler)
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("live-feed-broadcaster listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("http server error")
		}
	}()

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// 5. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)
	log.Info().Msg("live-feed-broadcaster stopped")
}
