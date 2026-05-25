// Package main is the native-policy-syncer service (Phase 11).
//
// It reads active platform policies from the control plane, translates them
// into engine-native constructs (views, RLS predicates, DDM policies, etc.)
// via pkg/connectors, and writes results to the engine_sync_state and
// native_enforcement_log tables.
//
// The service runs on a 1-hour ticker and also responds to POST /sync/:datasource_id
// for manual trigger.
//
// Feature flag: multi-db (checked at startup + handler level).
// Port: 8085
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
)

const (
	serviceName = "native-policy-syncer"
	featureFlag = "multi-db"
)

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. OTel — must be first.
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
	defer tel.Shutdown(context.Background()) //nolint:errcheck

	tracer := otel.Tracer(serviceName)

	// 2. Feature flag.
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

	// 3. Control-plane DB.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("PostgreSQL ping failed")
	}
	log.Info().Msg("control-plane database: connected")

	// 4. Audit client.
	auditClient := pkgaudit.New(pkgaudit.Config{
		Brokers:     []string{getEnv("KAFKA_BROKERS", "localhost:9092")},
		TopicSystem: getEnv("KAFKA_TOPIC_SYSTEM", "audit.system"),
		Service:     serviceName,
		HMACKey:     []byte(getEnv("AUDIT_HMAC_KEY", "")),
		Enabled:     getEnv("KAFKA_BROKERS", "") != "",
		Log:         log.Logger,
	})
	defer auditClient.Close() //nolint:errcheck

	// 5. Metrics.
	m := newMetrics()

	// 6. Syncer.
	syncer := newSyncer(pool, auditClient, tracer, log.Logger, cfg, m, ff)

	// 7. HTTP server.
	mux := http.NewServeMux()
	registerHandlers(mux, pool, syncer, ff, log.Logger)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("native-policy-syncer HTTP listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// 8. Background sync ticker.
	go syncer.RunLoop(ctx)

	// 9. Wait for shutdown.
	<-ctx.Done()
	log.Info().Msg("shutdown signal received — draining")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("HTTP shutdown error")
	}
	log.Info().Msg("native-policy-syncer stopped")
}

// registerHandlers wires all HTTP handlers.
func registerHandlers(
	mux *http.ServeMux,
	pool *pgxpool.Pool,
	syncer *Syncer,
	ff *featureflags.Client,
	log zerolog.Logger,
) {
	// Health endpoints.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Prometheus metrics.
	mux.Handle("GET /metrics", promhttp.Handler())

	// Manual sync trigger.
	mux.HandleFunc("POST /sync/{datasource_id}", func(w http.ResponseWriter, r *http.Request) {
		if !ff.IsEnabled(featureFlag) {
			http.Error(w, `{"error":"feature_disabled"}`, http.StatusNotFound)
			return
		}
		dsID := r.PathValue("datasource_id")
		if dsID == "" {
			http.Error(w, `{"error":"missing datasource_id"}`, http.StatusBadRequest)
			return
		}
		log.Info().Str("datasource_id", dsID).Msg("manual sync triggered")
		if err := syncer.SyncDataSource(r.Context(), dsID); err != nil {
			log.Error().Err(err).Str("datasource_id", dsID).Msg("manual sync failed")
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Sync state query.
	mux.HandleFunc("GET /sync-state", func(w http.ResponseWriter, r *http.Request) {
		if !ff.IsEnabled(featureFlag) {
			http.Error(w, `{"error":"feature_disabled"}`, http.StatusNotFound)
			return
		}
		// Returns JSON array of engine_sync_state rows (simplified).
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"not_implemented"}`))
	})
}
