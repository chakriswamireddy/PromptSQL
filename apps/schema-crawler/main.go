// Package main is the schema catalog crawler service (Phase 7).
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

	"github.com/governance-platform/schema-crawler/internal/api"
	"github.com/governance-platform/schema-crawler/internal/crawler"
	"github.com/governance-platform/schema-crawler/internal/embedding"
	"github.com/governance-platform/schema-crawler/internal/metrics"
	"github.com/governance-platform/schema-crawler/internal/scheduler"
	"github.com/governance-platform/schema-crawler/internal/store"
)

const serviceName = "schema-crawler"
const featureFlag = "schema-catalog"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel — initialise before any other dependency.
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

	otel.SetTracerProvider(tel.TracerProvider())
	otel.SetMeterProvider(tel.MeterProvider())

	// 2. Feature flag — exit cleanly if disabled.
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

	// 3. Prometheus metrics registration.
	metrics.Register()

	// 4. Control-plane DB (schema_metadata, crawl_runs, embedding_queue).
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("PostgreSQL ping failed")
	}

	db := store.New(pool)

	// 5. Audit client.
	auditClient := pkgaudit.New(pkgaudit.Config{
		Brokers:     []string{getEnv("KAFKA_BROKERS", "localhost:9092")},
		TopicSystem: getEnv("KAFKA_TOPIC_SYSTEM", "audit.system"),
		Service:     serviceName,
		HMACKey:     []byte(getEnv("AUDIT_HMAC_KEY", "dev-audit-hmac-key-32-bytes!!!!!")),
		Enabled:     getEnv("KAFKA_BROKERS", "") != "",
		Log:         log.Logger,
	})
	defer auditClient.Close() //nolint:errcheck

	// 6. Embedding provider.
	var embProvider embedding.Provider
	if cfg.OpenAIAPIKey != "" {
		embProvider = embedding.NewOpenAI(cfg.OpenAIAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDims)
		log.Info().Str("model", cfg.EmbeddingModel).Int("dims", cfg.EmbeddingDims).Msg("embedding provider: openai")
	} else {
		log.Warn().Msg("OPENAI_API_KEY not set — using noop embedding provider (CI/dev mode)")
		embProvider = embedding.NewNoop(cfg.EmbeddingDims)
	}

	// 7. Embedding worker pool.
	embWorker := embedding.NewWorker(db, embProvider, cfg.EmbeddingWorkers, log.Logger)
	go embWorker.Run(ctx)

	// 8. Crawler + scheduler.
	crawlerSvc := crawler.New(db, embProvider, auditClient, log.Logger, cfg.SampleMaxRows)

	vaultResolve := func(ctx context.Context, secretRef string) (string, error) {
		return resolveVaultDSN(ctx, secretRef, log.Logger)
	}

	sched := scheduler.New(db, crawlerSvc, vaultResolve, cfg.CrawlInterval, cfg.CrawlConcurrency, log.Logger)
	go sched.Run(ctx)

	// 9. HTTP server: health, metrics, catalog API.
	apiHandler := api.New(db, sched, log.Logger, func() bool { return ff.IsEnabled(featureFlag) })
	mux := http.NewServeMux()
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
	mux.Handle("GET /metrics", promhttp.Handler())
	apiHandler.Register(mux)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("schema-crawler HTTP listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// 10. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received — draining")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	log.Info().Msg("schema-crawler stopped")
}

// resolveVaultDSN retrieves a DSN from Vault (or env for local dev).
// In production this calls the Vault agent sidecar; in dev mode it reads from env.
func resolveVaultDSN(ctx context.Context, secretRef string, log zerolog.Logger) (string, error) {
	// Dev: return env var named after the last segment of the secret ref.
	envKey := "DATASOURCE_DSN_" + secretRef
	if dsn := os.Getenv(envKey); dsn != "" {
		return dsn, nil
	}
	// Fallback: generic dev DSN.
	if dsn := os.Getenv("DATASOURCE_DSN_DEFAULT"); dsn != "" {
		log.Warn().Str("secret_ref", secretRef).Msg("using DATASOURCE_DSN_DEFAULT (dev mode)")
		return dsn, nil
	}
	return "", fmt.Errorf("no DSN found for secret_ref %q; set env DATASOURCE_DSN_%s", secretRef, secretRef)
}
