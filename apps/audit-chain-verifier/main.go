package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"

	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
)

const (
	serviceName = "audit-chain-verifier"
	featureFlag = "audit-pipeline"
)

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pool.Close()

	verifier, err := newVerifier(cfg, pool, log)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init verifier")
	}

	// Health + metrics endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
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

	// Schedule verifier runs.
	hourlyTick := time.NewTicker(time.Hour)
	// Daily sample at 02:00 UTC. For simplicity use a 24h ticker offset.
	dailyTick := time.NewTicker(24 * time.Hour)
	defer hourlyTick.Stop()
	defer dailyTick.Stop()

	// Run immediately on startup so the first hour isn't a blank.
	go func() {
		if err := verifier.RunHourly(ctx); err != nil {
			log.Error().Err(err).Msg("hourly verifier failed")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	for {
		select {
		case <-hourlyTick.C:
			log.Info().Msg("running hourly hash-chain verification")
			if err := verifier.RunHourly(ctx); err != nil {
				log.Error().Err(err).Msg("hourly verifier failed")
			}
		case <-dailyTick.C:
			log.Info().Msg("running daily sampled hash-chain verification")
			if err := verifier.RunDailySample(ctx); err != nil {
				log.Error().Err(err).Msg("daily verifier failed")
			}
		case <-quit:
			log.Info().Msg("shutdown signal received")
			cancel()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutCancel()
			_ = httpSrv.Shutdown(shutCtx)
			log.Info().Msg("audit-chain-verifier stopped")
			return
		}
	}
}
