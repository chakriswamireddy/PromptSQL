package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/governance-platform/api-gateway/internal/admin"
	"github.com/governance-platform/api-gateway/internal/auth"
	"github.com/governance-platform/api-gateway/internal/outbox"
	pkgauth "github.com/governance-platform/pkg/auth"
	"github.com/governance-platform/pkg/featureflags"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/telemetry"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const serviceName = "api-gateway"
const featureFlag = "api-gateway"
const authFeatureFlag = "authn-session"
const adminFeatureFlag = "admin-console-v1"

func main() {
	cfg := loadConfig()
	log := logging.New(serviceName, cfg.Version, cfg.Environment)

	ctx := context.Background()

	// OTel must be initialised before any other component.
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

	// Feature flag — skip registration if the gateway flag is disabled.
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

	authEnabled := ff.IsEnabled(authFeatureFlag)

	// ── PostgreSQL ──────────────────────────────────────────────────────────────
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create DB pool")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("DB ping failed")
	}
	log.Info().Msg("database connected")

	// ── Redis ───────────────────────────────────────────────────────────────────
	rdbOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid REDIS_URL")
	}
	rdb := redis.NewClient(rdbOpts)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("Redis ping failed — auth features will be degraded")
	} else {
		log.Info().Msg("redis connected")
	}

	// ── JWT service ─────────────────────────────────────────────────────────────
	var jwtSvc *pkgauth.JWTService
	if cfg.JWTPrivateKeyB64 != "" {
		priv, pub, err := pkgauth.ParseEd25519PrivateKeyB64(cfg.JWTPrivateKeyB64)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid JWT_ED25519_PRIVATE_KEY")
		}
		jwtSvc = pkgauth.NewJWTService(pkgauth.JWTConfig{
			PrivateKey: priv,
			PublicKey:  pub,
			Issuer:     cfg.JWTIssuer,
			Audience:   cfg.JWTAudience,
		})
		log.Info().Msg("JWT service initialised (signing + verification)")
	} else {
		// Verify-only mode for non-gateway services; log a warning in dev.
		log.Warn().Msg("JWT_ED25519_PRIVATE_KEY not set — token signing disabled")
		jwtSvc = pkgauth.NewJWTService(pkgauth.JWTConfig{
			Issuer:   cfg.JWTIssuer,
			Audience: cfg.JWTAudience,
		})
	}

	// ── JTI store ───────────────────────────────────────────────────────────────
	jtiStore := pkgauth.NewJTIStore(rdb, 11*time.Minute)

	// ── HMAC service ────────────────────────────────────────────────────────────
	var hmacSvc *pkgauth.HMACService
	hmacSecrets := parseHMACSecrets(cfg.HMACSecrets)
	if len(hmacSecrets) > 0 {
		hmacSvc, err = pkgauth.NewHMACService(hmacSecrets)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to initialise HMAC service")
		}
	} else {
		log.Warn().Msg("HMAC_SECRETS not set — service-to-service propagation disabled")
	}

	// ── Auth sub-services ───────────────────────────────────────────────────────
	roleResolver := auth.NewRoleResolver(pool, rdb)
	refreshStore := auth.NewRefreshStore(pool)
	mfaService := auth.NewMFAService(pool, cfg.TOTPIssuer)
	authHandler := auth.NewHandler(pool, jwtSvc, refreshStore, mfaService, roleResolver, authEnabled)

	authMiddleware := auth.Middleware(auth.Dependencies{
		JWT:   jwtSvc,
		JTI:   jtiStore,
		HMAC:  hmacSvc,
		Roles: roleResolver,
		Pool:  pool,
		Redis: rdb,
	})

	// ── Routes ──────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Health endpoints — no auth.
	mux.HandleFunc("GET /healthz", handleLiveness)
	mux.HandleFunc("GET /readyz", makeReadinessHandler(pool, rdb))

	// JWKS endpoint — public, cached.
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		data, err := jwtSvc.JWKSPayload()
		if err != nil {
			http.Error(w, `{"code":"internal_error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(data)
	})

	// Auth endpoints — no middleware on login/refresh (user has no token yet).
	mux.HandleFunc("POST /v1/auth/login", authHandler.Login)
	mux.HandleFunc("POST /v1/auth/refresh", authHandler.Refresh)

	// Auth endpoints — require valid JWT.
	mux.Handle("POST /v1/auth/logout", authMiddleware(http.HandlerFunc(authHandler.Logout)))
	mux.Handle("POST /v1/auth/logout-everywhere", authMiddleware(http.HandlerFunc(authHandler.LogoutEverywhere)))
	mux.Handle("GET /v1/auth/sessions", authMiddleware(http.HandlerFunc(authHandler.Sessions)))
	mux.Handle("DELETE /v1/auth/sessions/{id}", authMiddleware(http.HandlerFunc(authHandler.DeleteSession)))
	mux.Handle("POST /v1/auth/mfa/enroll", authMiddleware(http.HandlerFunc(authHandler.MFAEnroll)))
	mux.Handle("POST /v1/auth/mfa/verify", authMiddleware(http.HandlerFunc(authHandler.MFAVerify)))
	mux.Handle("POST /v1/auth/mfa/disable", authMiddleware(http.HandlerFunc(authHandler.MFADisable)))

	// ── Admin BFF routes (Phase 4) ──────────────────────────────────────────────
	if ff.IsEnabled(adminFeatureFlag) {
		// Connect to PDP for simulator.
		pdpCC, pdpErr := grpc.NewClient(cfg.PDPAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if pdpErr != nil {
			log.Warn().Err(pdpErr).Msg("PDP connection failed — simulator will be degraded")
		}

		var pdpClient pdpv1.PDPClient
		if pdpCC != nil {
			pdpClient = pdpv1.NewPDPClient(pdpCC)
		}

		policiesH := admin.NewPoliciesHandler(pool, pdpClient)
		simulatorH := admin.NewSimulatorHandler(pool, pdpClient)
		usersH := admin.NewUsersHandler(pool)
		auditH := admin.NewAuditHandler(pool)
		dsH := admin.NewDataSourcesHandler(pool)

		// Helper: wrap handler with auth middleware.
		adminRoute := func(h http.HandlerFunc) http.Handler {
			return authMiddleware(http.HandlerFunc(h))
		}

		// Policies.
		mux.Handle("GET /v1/admin/{tenantSlug}/policies", adminRoute(policiesH.List))
		mux.Handle("POST /v1/admin/{tenantSlug}/policies", adminRoute(policiesH.Create))
		mux.Handle("GET /v1/admin/{tenantSlug}/policies/{id}", adminRoute(policiesH.Get))
		mux.Handle("PUT /v1/admin/{tenantSlug}/policies/{id}", adminRoute(policiesH.Update))
		mux.Handle("POST /v1/admin/{tenantSlug}/policies/{id}/submit", adminRoute(policiesH.Submit))
		mux.Handle("POST /v1/admin/{tenantSlug}/policies/{id}/approve", adminRoute(policiesH.Approve))
		mux.Handle("POST /v1/admin/{tenantSlug}/policies/{id}/archive", adminRoute(policiesH.Archive))

		// Simulator.
		mux.Handle("POST /v1/admin/{tenantSlug}/policies/simulate", adminRoute(simulatorH.Simulate))
		mux.Handle("POST /v1/admin/{tenantSlug}/policies/simulate/diff", adminRoute(simulatorH.SimulateDiff))

		// Users.
		mux.Handle("GET /v1/admin/{tenantSlug}/users", adminRoute(usersH.List))
		mux.Handle("POST /v1/admin/{tenantSlug}/users/{userID}/suspend", adminRoute(usersH.Suspend))
		mux.Handle("PUT /v1/admin/{tenantSlug}/users/{userID}/roles", adminRoute(usersH.UpdateRoles))

		// Roles (list only in Phase 4; mutations in later phases).
		mux.Handle("GET /v1/admin/{tenantSlug}/roles", adminRoute(func(w http.ResponseWriter, r *http.Request) {
			sess := admin.SessionFromContext(r.Context())
			if sess == nil {
				http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			// Thin shim — full impl in admin/roles.go.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[]}`))
		}))

		// Audit.
		mux.Handle("GET /v1/admin/{tenantSlug}/audit/policies", adminRoute(auditH.PolicyAudit))
		mux.Handle("GET /v1/admin/{tenantSlug}/audit/access", adminRoute(auditH.AccessAudit))

		// Data sources.
		mux.Handle("GET /v1/admin/{tenantSlug}/data-sources", adminRoute(dsH.List))

		// Outbox relay — runs as a background goroutine.
		relay := outbox.New(pool, rdb, log)
		go func() {
			relay.Run(context.Background())
		}()

		log.Info().Msg("admin-console-v1 feature enabled — admin routes registered")
	} else {
		log.Info().Str("flag", adminFeatureFlag).Msg("admin-console-v1 disabled")
	}

	// ── DB token endpoints (Phase 6 — PEP PostgreSQL Proxy) ───────────────────
	if ff.IsEnabled("pep-postgres-proxy") {
		proxyHost := getEnvDefault("PROXY_ADVERTISE_HOST", "proxy.platform.svc.cluster.local")
		proxyPort := 5433
		dbTokenH := auth.NewDBTokenHandler(rdb, proxyHost, proxyPort)
		mux.Handle("POST /v1/db-token", authMiddleware(http.HandlerFunc(dbTokenH.IssueToken)))
		mux.Handle("DELETE /v1/db-token", authMiddleware(http.HandlerFunc(dbTokenH.RevokeToken)))
		log.Info().Msg("pep-postgres-proxy enabled — /v1/db-token routes registered")
	}

	// ── Retrieval endpoints (Phase 8 — Permission-Aware Retrieval) ────────────
	if ff.IsEnabled("permission-aware-retrieval") {
		retrievalBase := getEnvDefault("RETRIEVAL_SERVICE_ADDR", "http://retrieval-service:8083")
		retrievalProxy := newReverseProxy(retrievalBase)

		// Forward session headers so the retrieval service can enforce ACLs.
		forwardRetrieval := func(h http.Handler) http.Handler {
			return authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sess := auth.SessionFromContext(r.Context())
				if sess == nil {
					http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
				r.Header.Set("X-Tenant-ID", sess.TenantID)
				r.Header.Set("X-User-ID", sess.UserID)
				r.Header.Set("X-User-Roles", joinRoles(sess.Roles))
				h.ServeHTTP(w, r)
			}))
		}

		mux.Handle("POST /v1/retrieval/snapshot", forwardRetrieval(retrievalProxy))
		mux.Handle("POST /v1/retrieval/docs", forwardRetrieval(retrievalProxy))
		mux.Handle("POST /v1/retrieval/route", forwardRetrieval(retrievalProxy))
		mux.Handle("POST /v1/retrieval/explain", forwardRetrieval(retrievalProxy))
		log.Info().Str("upstream", retrievalBase).Msg("permission-aware-retrieval enabled — /v1/retrieval/* routes registered")
	}

	// ── AI PEP Graph routes (Phase 10 — NL → Safe SQL) ───────────────────────
	if ff.IsEnabled("ai-pep-graph") {
		orchestratorBase := getEnvDefault("AI_ORCHESTRATOR_ADDR", "http://ai-orchestrator:8084")
		orchProxy := newReverseProxy(orchestratorBase)

		// Forward session + db-token headers so the orchestrator knows who is asking.
		forwardPep := func(h http.Handler) http.Handler {
			return authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sess := auth.SessionFromContext(r.Context())
				if sess == nil {
					http.Error(w, `{"code":"unauthorized"}`, http.StatusUnauthorized)
					return
				}
				r.Header.Set("X-Tenant-Id", sess.TenantID)
				r.Header.Set("X-User-Id", sess.UserID)
				// X-DB-Token passed through from client (already validated by proxy on use)
				h.ServeHTTP(w, r)
			}))
		}

		// SSE — disable read/write timeouts for the streaming endpoint
		mux.Handle("POST /v1/ai/pep/ask",                         forwardPep(orchProxy))
		mux.Handle("GET /v1/ai/pep/sessions/{id}",                forwardPep(orchProxy))
		mux.Handle("POST /v1/ai/pep/feedback",                    forwardPep(orchProxy))
		mux.Handle("GET /v1/ai/pep/saved-questions",              forwardPep(orchProxy))
		mux.Handle("POST /v1/ai/pep/saved-questions",             forwardPep(orchProxy))
		mux.Handle("POST /v1/ai/pep/saved-questions/{id}/run",    forwardPep(orchProxy))

		// PAP routes (Phase 9) — also forward via ai-orchestrator if not already registered
		mux.Handle("POST /v1/ai/pap/draft",   forwardPep(orchProxy))
		mux.Handle("POST /v1/ai/pap/approve", forwardPep(orchProxy))
		mux.Handle("POST /v1/ai/pap/explain", forwardPep(orchProxy))

		log.Info().Str("upstream", orchestratorBase).Msg("ai-pep-graph enabled — /v1/ai/pep/* routes registered")
	}

	// Catch-all for unimplemented v1 routes.
	mux.HandleFunc("GET /v1/", handleNotImplemented)

	srv := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Bool("auth_enabled", authEnabled).Msg("api-gateway listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	<-quit
	log.Info().Msg("shutdown signal received — draining")

	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}
	log.Info().Msg("api-gateway stopped")
}

func handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func makeReadinessHandler(pool *pgxpool.Pool, rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, `{"ready":false,"reason":"db"}`, http.StatusServiceUnavailable)
			return
		}
		if err := rdb.Ping(ctx).Err(); err != nil {
			http.Error(w, `{"ready":false,"reason":"redis"}`, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func getEnvDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func handleNotImplemented(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, `{"code":"not_implemented","message":"coming in a later phase"}`, http.StatusNotImplemented)
}
