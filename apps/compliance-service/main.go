package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/accessreview"
	"github.com/org/platform/apps/compliance-service/internal/billing"
	"github.com/org/platform/apps/compliance-service/internal/evidence"
	"github.com/org/platform/apps/compliance-service/internal/gdpr"
	"github.com/org/platform/apps/compliance-service/internal/health"
	"github.com/org/platform/apps/compliance-service/internal/scim"
	"github.com/org/platform/apps/compliance-service/internal/siem"
	"github.com/org/platform/apps/compliance-service/internal/store"
	"github.com/org/platform/apps/compliance-service/internal/unleash"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)
	defer log.Sync() //nolint

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Feature flag check — exit cleanly if compliance-ga is off.
	ff := unleash.New(cfg.UnleashURL, cfg.UnleashToken)
	if !ff.IsEnabled("compliance-ga") {
		log.Info("compliance-ga feature flag is disabled; exiting cleanly")
		os.Exit(0)
	}

	// OTel init (traces + metrics).
	shutdown, err := initOTel(ctx, cfg.OTelEndpoint, "compliance-service")
	if err != nil {
		log.Fatal("otel init", zap.Error(err))
	}
	defer shutdown(context.Background()) //nolint

	db, err := store.NewDB(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal("db connect", zap.Error(err))
	}
	defer db.Close()

	// Build sub-handlers.
	arH := accessreview.NewHandler(db, log)
	evH := evidence.NewHandler(db, log)
	hlH := health.NewHandler(db, log)
	siemH := siem.NewHandler(db, log)
	scimH := scim.NewHandler(db, log)
	bilH := billing.NewHandler(db, cfg.StripeSecretKey, cfg.StripeWebhookSecret, log)
	gdprH := gdpr.NewHandler(db, log)

	// Background jobs.
	go health.RunDailyRollup(ctx, db, log)
	go evidence.RunFreshnessChecker(ctx, db, log)
	go accessreview.RunQuarterlyGenerator(ctx, db, log)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(otelMiddleware("compliance-service"))
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(db))
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/v1", func(r chi.Router) {
		// Access reviews
		r.Post("/admin/{tenant_id}/access-reviews/generate", arH.Generate)
		r.Get("/admin/{tenant_id}/access-reviews", arH.List)
		r.Get("/admin/{tenant_id}/access-reviews/{review_id}", arH.Get)
		r.Put("/admin/{tenant_id}/access-reviews/{review_id}/entries/{entry_id}", arH.Certify)

		// Compliance evidence
		r.Get("/admin/{tenant_id}/compliance/evidence", evH.List)
		r.Post("/admin/{tenant_id}/compliance/evidence/collect", evH.Collect)

		// Customer health
		r.Get("/admin/{tenant_id}/health-score", hlH.Get)
		r.Get("/admin/{tenant_id}/health-score/history", hlH.History)

		// SIEM export
		r.Get("/admin/{tenant_id}/audit/export/siem", siemH.Export)

		// GDPR SAR
		r.Post("/admin/{tenant_id}/gdpr/requests", gdprH.Submit)
		r.Get("/admin/{tenant_id}/gdpr/requests", gdprH.List)
		r.Put("/admin/{tenant_id}/gdpr/requests/{request_id}/status", gdprH.UpdateStatus)

		// Sub-processors (public)
		r.Get("/trust/subprocessors", subprocessorsHandler(db, log))

		// Compliance modes (per-tenant settings)
		r.Get("/admin/{tenant_id}/compliance/modes", modesGetHandler(db, log))
		r.Put("/admin/{tenant_id}/compliance/modes", modesUpdateHandler(db, log))

		// Billing webhook (verified by Stripe signature)
		r.Post("/billing/webhook", bilH.Webhook)
		r.Get("/admin/{tenant_id}/billing/subscription", bilH.GetSubscription)

		// SCIM 2.0 provisioning
		r.Route("/scim/v2/tenants/{tenant_id}", func(r chi.Router) {
			r.Use(scimH.AuthMiddleware)
			r.Get("/Users", scimH.ListUsers)
			r.Post("/Users", scimH.CreateUser)
			r.Get("/Users/{user_id}", scimH.GetUser)
			r.Put("/Users/{user_id}", scimH.ReplaceUser)
			r.Patch("/Users/{user_id}", scimH.PatchUser)
			r.Delete("/Users/{user_id}", scimH.DeleteUser)
			r.Get("/Groups", scimH.ListGroups)
			r.Post("/Groups", scimH.CreateGroup)
			r.Get("/Groups/{group_id}", scimH.GetGroup)
			r.Put("/Groups/{group_id}", scimH.ReplaceGroup)
			r.Patch("/Groups/{group_id}", scimH.PatchGroup)
			r.Delete("/Groups/{group_id}", scimH.DeleteGroup)
			r.Get("/ServiceProviderConfig", scimH.ServiceProviderConfig)
		})

		// SCIM token management
		r.Post("/admin/{tenant_id}/scim/tokens", scimH.CreateToken)
		r.Get("/admin/{tenant_id}/scim/tokens", scimH.ListTokens)
		r.Delete("/admin/{tenant_id}/scim/tokens/{token_id}", scimH.RevokeToken)
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("compliance-service listening", zap.String("port", cfg.Port))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("listen", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	log.Info("shutting down compliance-service")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Error("graceful shutdown", zap.Error(err))
	}
	log.Info("compliance-service stopped")
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok")) //nolint
}

func readyz(db *store.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint
	}
}
