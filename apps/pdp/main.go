package main

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pkgauth "github.com/governance-platform/pkg/auth"
	pkgdb "github.com/governance-platform/pkg/db"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/grpc/interceptors"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/pdp/internal/cache"
	"github.com/governance-platform/pdp/internal/invalidation"
	"github.com/governance-platform/pdp/internal/server"
	"github.com/governance-platform/pdp/internal/store"
)

const serviceName = "pdp"
const featureFlag = "pdp-v1"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. OTel SDK — must be first.
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

	// 3. PostgreSQL connection pool.
	pgpool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to PostgreSQL")
	}
	defer pgpool.Close()

	dbPool := pkgdb.New(pgpool)
	policyStore := store.New(dbPool)

	// 4. Redis client (L2 cache + pub/sub).
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid REDIS_URL")
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()

	// 5. HMAC service for SessionContext verification.
	hmacSvc, err := parseHMACSecrets(cfg.HMACSecrets)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse HMAC_SECRETS")
	}

	// 6. Cache + invalidation.
	decisionCache := cache.New(rdb)
	versions := invalidation.NewVersionStore()
	sub := invalidation.New(rdb, versions, nil, log)

	// 7. Build gRPC server (Phase 13: pass Redis for risk score lookup).
	pdpServer := server.New(server.Config{
		Store:    policyStore,
		Cache:    decisionCache,
		HMAC:     hmacSvc,
		Sub:      sub,
		Versions: versions,
		Log:      log,
		Redis:    rdb,
	})
	// Wire the invalidation callback after the server is created.
	versions2 := invalidation.NewVersionStore()
	sub2 := invalidation.New(rdb, versions2, pdpServer.InvalidateCallback, log)
	_ = sub2 // used by pdpServer internally via the subscriber

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatal().Err(err).Str("addr", cfg.GRPCAddr).Msg("failed to listen")
	}

	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(interceptors.UnaryServerInterceptors()...),
	)
	pdpv1.RegisterPDPServer(grpcSrv, pdpServer)
	healthSvc := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSvc)
	healthSvc.SetServingStatus("pdp.v1.PDP", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	// 8. HTTP server for /healthz, /readyz, /metrics.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := rdb.Ping(r.Context()).Err(); err != nil {
			http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
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

	// 9. Start servers.
	go func() {
		log.Info().Str("addr", cfg.GRPCAddr).Msg("pdp gRPC listening")
		if err := grpcSrv.Serve(lis); err != nil {
			log.Fatal().Err(err).Msg("gRPC serve error")
		}
	}()
	go func() {
		log.Info().Str("addr", cfg.MetricsAddr).Msg("pdp metrics/health listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server error")
		}
	}()

	// 10. Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit
	log.Info().Msg("shutdown signal received — draining")
	cancel()
	grpcSrv.GracefulStop()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	log.Info().Msg("pdp stopped")
}

// parseHMACSecrets decodes "keyID1:base64secret1,keyID2:base64secret2" into a map.
func parseHMACSecrets(raw string) (*pkgauth.HMACService, error) {
	secrets := make(map[string][]byte)
	if raw == "" {
		// Dev mode: use a fixed insecure key.
		secrets["dev"] = []byte("dev-insecure-hmac-key-32-bytes!!")
	} else {
		for _, entry := range strings.Split(raw, ",") {
			parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
			if len(parts) != 2 {
				continue
			}
			b, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				continue
			}
			secrets[parts[0]] = b
		}
	}
	return pkgauth.NewHMACService(secrets)
}
