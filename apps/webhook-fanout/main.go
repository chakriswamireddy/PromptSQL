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
	serviceName = "webhook-fanout"
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

	// 2. Feature flag.
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

	// 3. DB store.
	store, err := newStore(ctx, cfg.DatabaseDSN, log)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer store.Close()

	// 4. Vault client.
	vault := newVaultClient(cfg.VaultAddr, cfg.VaultToken)

	// 5. Deliverer + fanout.
	deliverer := newDeliverer(cfg, log)
	fanout := newFanout(store, vault, deliverer, cfg, log)

	// 6. Kafka consumer.
	consumer := newKafkaConsumer(fanout, cfg, log)
	consumer.Run(ctx)
	defer consumer.Close()

	// 7. Saved-query scheduler.
	scheduler := newScheduler(store, cfg, log)
	go scheduler.Run(ctx)

	// 8. Health + metrics.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("webhook-fanout listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("http server error")
		}
	}()

	// 9. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	log.Info().Msg("webhook-fanout stopped")
}
