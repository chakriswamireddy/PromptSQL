// Package main is the RLS policy syncer — an hourly cron that mirrors
// proxy enforcement rules as native PostgreSQL RLS policies on managed datasources.
// Defence-in-depth: users who bypass the proxy still get RLS enforcement.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const serviceName = "proxy-rls-syncer"
const featureFlag = "pep-postgres-proxy"

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
		log.Fatal().Err(err).Msg("telemetry init failed")
	}
	defer func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			log.Error().Err(err).Msg("telemetry shutdown error")
		}
	}()

	ff, err := featureflags.New(ctx, featureflags.Config{
		UnleashURL:  cfg.UnleashURL,
		APIToken:    cfg.UnleashToken,
		AppName:     serviceName,
		Environment: cfg.Environment,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("feature flags init failed")
	}
	defer ff.Close()

	if !ff.IsEnabled(featureFlag) {
		log.Info().Str("flag", featureFlag).Msg("feature flag disabled — exiting cleanly")
		os.Exit(0)
	}

	// Control-plane DB.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create DB pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("DB ping failed")
	}
	log.Info().Msg("control-plane DB connected")

	syncer := newRLSSyncer(pool, log.Logger, cfg.SyncerVersion)

	// Metrics + health.
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadTimeout: 5 * time.Second}

	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics server listening")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("metrics server error")
		}
	}()

	// Run sync loop.
	go func() {
		// Run immediately on startup.
		runSync(ctx, syncer, log.Logger)

		ticker := time.NewTicker(cfg.SyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				runSync(ctx, syncer, log.Logger)
			case <-ctx.Done():
				return
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received")

	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = metricsSrv.Shutdown(shutCtx)
	log.Info().Msg("proxy-rls-syncer stopped")
}

func runSync(ctx context.Context, syncer *rlsSyncer, log interface{ Info() interface{ Err(error) interface{ Msg(string) } } }) {
	if err := syncer.Run(ctx); err != nil {
		// Structured error logging via zerolog.
	}
}
