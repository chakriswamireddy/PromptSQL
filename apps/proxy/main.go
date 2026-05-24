// Package main is the PostgreSQL wire-protocol Policy Enforcement Point proxy.
// Phase 6: full wire-protocol implementation with Calcite sidecar integration.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pkgaudit "github.com/governance-platform/pkg/audit"
	calcitepb "github.com/governance-platform/pkg/calcitepb"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serviceName = "proxy"
const featureFlag = "pep-postgres-proxy"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// OTel must be first.
	tel, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:    serviceName,
		ServiceVersion: cfg.Version,
		Environment:    cfg.Environment,
		OTLPEndpoint:   cfg.OTLPEndpoint,
		SamplingRate:   cfg.SamplingRate,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise telemetry")
	}
	defer func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			log.Error().Err(err).Msg("telemetry shutdown error")
		}
	}()

	// Feature flags.
	ff, err := featureflags.New(ctx, featureflags.Config{
		UnleashURL:  cfg.UnleashURL,
		APIToken:    cfg.UnleashToken,
		AppName:     serviceName,
		Environment: cfg.Environment,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise feature flags")
	}
	defer ff.Close()

	if !ff.IsEnabled(featureFlag) {
		log.Info().Str("flag", featureFlag).Msg("feature flag disabled — exiting cleanly")
		os.Exit(0)
	}

	// Redis — token cache.
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

	// PDP gRPC client.
	pdpCC, err := grpc.NewClient(cfg.PDPAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Str("addr", cfg.PDPAddr).Msg("failed to connect to PDP")
	}
	pdpClient := pdpv1.NewPDPClient(pdpCC)

	// Calcite sidecar gRPC client.
	calciteCC, err := grpc.NewClient(cfg.CalciteAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Str("addr", cfg.CalciteAddr).Msg("failed to connect to Calcite sidecar")
	}
	calciteClient := calcitepb.NewCalciteRewriterClient(calciteCC)

	// Audit producer.
	auditor, err := pkgaudit.NewClient(pkgaudit.Config{
		KafkaBrokers: getEnv("KAFKA_BROKERS", "localhost:9092"),
		ServiceName:  serviceName,
	})
	if err != nil {
		log.Warn().Err(err).Msg("audit producer init failed — audit events will be dropped")
	}

	// Wire proxy server.
	srv := newServer(cfg, pdpClient, calciteClient, rdb, auditor, log.Logger)

	// Metrics + health HTTP server.
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		// Not ready if Calcite sidecar is unreachable.
		pingCtx, cancelPing := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancelPing()
		if err := rdb.Ping(pingCtx).Err(); err != nil {
			http.Error(w, `{"ready":false,"reason":"redis"}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	metricsSrv := &http.Server{
		Addr:        cfg.MetricsAddr,
		Handler:     mux,
		ReadTimeout: 5 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	// Start metrics server.
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics/health server listening")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("metrics server error")
		}
	}()

	// Start proxy in background.
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- srv.ListenAndServe(ctx)
	}()

	select {
	case <-quit:
		log.Info().Msg("shutdown signal received — draining")
	case err := <-proxyErr:
		if err != nil {
			log.Fatal().Err(err).Msg("proxy listener error")
		}
	}

	cancel() // signal proxy to stop accepting

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()

	if err := metricsSrv.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("metrics server shutdown error")
	}
	srv.Shutdown()
	log.Info().Msg("proxy stopped")
}
