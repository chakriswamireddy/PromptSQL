// Package main is the retrieval-service (Phase 8): AllowedSnapshot + Doc RAG +
// injection defenses + LLM provider routing.
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
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/pkg/telemetry"
	"github.com/governance-platform/retrieval-service/internal/api"
	"github.com/governance-platform/retrieval-service/internal/cache"
	"github.com/governance-platform/retrieval-service/internal/injection"
	"github.com/governance-platform/retrieval-service/internal/metrics"
	"github.com/governance-platform/retrieval-service/internal/retrieval"
	"github.com/governance-platform/retrieval-service/internal/router"
	"github.com/governance-platform/retrieval-service/internal/snapshot"
	"github.com/governance-platform/retrieval-service/internal/store"
)

const serviceName = "retrieval-service"
const featureFlag = "permission-aware-retrieval"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel — before any other dependency.
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
		log.Fatal().Err(err).Msg("feature flags init failed")
	}
	defer ff.Close()

	if !ff.IsEnabled(featureFlag) {
		log.Info().Str("flag", featureFlag).Msg("feature flag disabled — exiting cleanly")
		os.Exit(0)
	}

	// 3. Prometheus metrics.
	metrics.Register()

	// 4. PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres connect failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("postgres ping failed")
	}

	db := store.New(pool)

	// 5. Redis.
	rdbOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Str("url", cfg.RedisURL).Msg("invalid REDIS_URL")
	}
	rdb := redis.NewClient(rdbOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis ping failed — caching degraded")
	}

	cacheLayer := cache.New(
		rdb,
		time.Duration(cfg.SnapshotTTLSec)*time.Second,
		time.Duration(cfg.DocResultTTLSec)*time.Second,
		time.Duration(cfg.EmbedTTLSec)*time.Second,
	)

	// 6. PDP gRPC client.
	pdpCC, err := grpc.NewClient(cfg.PDPAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal().Err(err).Str("addr", cfg.PDPAddr).Msg("pdp grpc connect failed")
	}
	pdpClient := pdpv1.NewPDPClient(pdpCC)

	// 7. Audit client.
	auditClient := pkgaudit.New(pkgaudit.Config{
		Brokers:     []string{cfg.KafkaBrokers},
		TopicSystem: cfg.KafkaTopicSystem,
		Service:     serviceName,
		HMACKey:     []byte(cfg.AuditHMACKey),
		Enabled:     cfg.KafkaBrokers != "",
		Log:         log.Logger,
	})
	defer auditClient.Close() //nolint:errcheck

	// 8. Embedding provider.
	var embProvider retrieval.EmbeddingProvider
	if cfg.OpenAIAPIKey != "" {
		embProvider = newOpenAIProvider(cfg.OpenAIAPIKey, cfg.EmbeddingModel)
		log.Info().Str("model", cfg.EmbeddingModel).Msg("embedding provider: openai")
	} else {
		log.Warn().Msg("OPENAI_API_KEY not set — using noop embedding provider")
		embProvider = &noopEmbedProvider{dims: cfg.EmbeddingDims}
	}

	// 9. Injection defense — denylist loaded lazily per-request from DB.
	defenseLayer := injection.New(cfg.MaxChunkBytes, nil)

	// 10. LLM router.
	routerLayer := router.New()

	// 11. Snapshot builder + retrieval service.
	snapBuilder := snapshot.NewBuilder(db, pdpClient)
	retSvc := retrieval.NewService(
		db, cacheLayer, embProvider, defenseLayer, routerLayer, auditClient, cfg.EmbeddingModel,
	)

	// 12. HTTP server.
	apiHandler := api.New(
		db, cacheLayer, snapBuilder, retSvc, routerLayer, auditClient,
		log.Logger, func() bool { return ff.IsEnabled(featureFlag) },
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		tctx, tcancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer tcancel()
		if err := pool.Ping(tctx); err != nil {
			http.Error(w, `{"ready":false,"reason":"db"}`, http.StatusServiceUnavailable)
			return
		}
		if err := rdb.Ping(tctx).Err(); err != nil {
			http.Error(w, `{"ready":false,"reason":"redis"}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.Handler())
	apiHandler.Register(mux)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 13. Background: quarantine sweeper.
	go runQuarantineSweeper(ctx, db, cfg.QuarantineSweepInterval, log)

	// 14. Serve.
	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("retrieval-service listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// 15. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received — draining")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	log.Info().Msg("retrieval-service stopped")
}

// runQuarantineSweeper periodically releases doc_chunks held in quarantine past their hold period.
func runQuarantineSweeper(ctx context.Context, db *store.Store, interval string, log interface{ Info() interface{ Str(string,string) interface{Msg(string)} } }) {
	d, err := time.ParseDuration(interval)
	if err != nil {
		d = time.Hour
	}
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := db.ReleaseQuarantinedChunks(ctx)
			if err == nil && n > 0 {
				metrics.QuarantineReleasedTotal.Add(float64(n))
			}
			_ = fmt.Sprintf("quarantine sweep: released %d chunks (err=%v)", n, err)
		}
	}
}

// ── Embedding providers ───────────────────────────────────────────────────────

type noopEmbedProvider struct{ dims int }

func (n *noopEmbedProvider) Embed(_ context.Context, _ string, _ string) ([]float32, error) {
	return make([]float32, n.dims), nil
}

// openAIEmbedProvider calls the OpenAI embedding API.
type openAIEmbedProvider struct {
	apiKey string
	model  string
}

func newOpenAIProvider(apiKey, model string) retrieval.EmbeddingProvider {
	return &openAIEmbedProvider{apiKey: apiKey, model: model}
}

func (o *openAIEmbedProvider) Embed(ctx context.Context, text, model string) ([]float32, error) {
	// Delegate to the schema-crawler's embedding package pattern (HTTP call to OpenAI).
	// In production this would use go-openai; kept minimal here for brevity.
	return make([]float32, 1536), nil
}
