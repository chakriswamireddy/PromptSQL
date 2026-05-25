package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/governance-platform/auto-responder/internal/api"
	"github.com/governance-platform/auto-responder/internal/breakglass"
	"github.com/governance-platform/auto-responder/internal/playbook"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/obligation"
	"github.com/governance-platform/pkg/telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

const serviceName = "auto-responder"
const featureFlag = "auto-response"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OTel — must be initialised first.
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

	// Feature flag gate.
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

	// PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create DB pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("DB ping failed")
	}
	log.Info().Msg("database connected")

	// Redis.
	rdbOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid REDIS_URL")
	}
	rdb := redis.NewClient(rdbOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("Redis ping failed — cache disabled")
	} else {
		log.Info().Msg("redis connected")
	}

	// Obligation token service.
	var obSvc *obligation.Service
	if cfg.ObligationKeyB64 != "" {
		keyBytes, err := base64.StdEncoding.DecodeString(cfg.ObligationKeyB64)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid OBLIGATION_HMAC_KEY_B64")
		}
		obSvc, err = obligation.New(keyBytes, 5*time.Minute)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to init obligation service")
		}
		log.Info().Msg("obligation service initialised")
	} else {
		log.Warn().Msg("OBLIGATION_HMAC_KEY_B64 not set — step-up obligations disabled")
	}

	// Stores.
	bgStore := breakglass.NewStore(pool)
	pbStore := playbook.NewStore(pool, rdb)

	// Break-glass auto-revoker (30s sweep).
	revoker := breakglass.NewRevoker(bgStore, 30*time.Second, log)
	go revoker.Run(ctx)

	// HTTP handler.
	h := api.New(bgStore, pbStore, obSvc, log)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Metrics server.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux}
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics server starting")
		if err := metricsServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// Main server.
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.Addr).Msg("auto-responder starting")
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutting down")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	_ = metricsServer.Shutdown(shutCtx)
	log.Info().Msg("shutdown complete")
}
