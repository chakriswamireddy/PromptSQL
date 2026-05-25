package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"

	"github.com/governance-platform/anomaly-detector/internal/api"
	"github.com/governance-platform/anomaly-detector/internal/baseline"
	"github.com/governance-platform/anomaly-detector/internal/consumer"
	"github.com/governance-platform/anomaly-detector/internal/scoring"
	"github.com/governance-platform/anomaly-detector/internal/sink"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
)

const serviceName = "anomaly-detector"
const featureFlag = "anomaly-detection"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel — must be initialised first.
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

	// 3. PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create DB pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("DB ping failed")
	}
	log.Info().Msg("database connected")

	// 4. Redis.
	rdbOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid REDIS_URL")
	}
	rdb := redis.NewClient(rdbOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatal().Err(err).Msg("redis ping failed")
	}
	log.Info().Msg("redis connected")

	// 5. Sinks.
	redisSink := sink.NewRedisSink(rdb, cfg.ScoreTTL)
	kafkaSink := sink.NewKafkaSink(cfg.KafkaBrokers, cfg.TopicRiskScored)
	defer kafkaSink.Close() //nolint:errcheck

	// 6. Baseline store.
	baselineStore := baseline.NewStore(pool)

	// 7. Consumer (statistical pipeline).
	cons := consumer.New(consumer.Config{
		KafkaBrokers:   cfg.KafkaBrokers,
		Topic:          cfg.TopicAuditAccess,
		ConsumerGroup:  cfg.ConsumerGroup,
		BatchSize:      cfg.BatchSize,
		BatchTimeout:   cfg.BatchTimeout,
		BaselineStore:  baselineStore,
		Redis:          redisSink,
		Kafka:          kafkaSink,
		WarmupDays:     cfg.WarmupDays,
		DecayHalfLife:  cfg.DecayHalfLifeHours,
		FlushPeriod:    cfg.BaselineFlushPeriod,
		DefaultWeights: scoring.DefaultWeights,
		Log:            log.Logger,
	})
	go cons.Run(ctx)
	defer cons.Close()

	// 8. REST API (risk score, calibration, overrides).
	apiHandler := api.NewHandler(pool, rdb, log.Logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		rCtx, rCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer rCancel()
		if err := pool.Ping(rCtx); err != nil {
			http.Error(w, `{"ready":false,"reason":"db"}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Risk score API — these are proxied through api-gateway under anomaly-detection flag.
	mux.HandleFunc("GET /v1/users/{userID}/risk-score", apiHandler.GetRiskScore)
	mux.HandleFunc("POST /v1/users/{userID}/risk-override", apiHandler.PostRiskOverride)
	mux.HandleFunc("GET /v1/risk/events", apiHandler.GetRiskEvents)
	mux.HandleFunc("GET /v1/risk/calibration", apiHandler.GetCalibration)
	mux.HandleFunc("PUT /v1/risk/calibration", apiHandler.PutCalibration)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("anomaly-detector API listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("API server error")
		}
	}()

	// 9. Metrics / health server.
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics server listening")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// 10. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Info().Msg("shutdown signal received — draining")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	_ = metricsSrv.Shutdown(shutCtx)

	log.Info().Msg("anomaly-detector stopped")
}
