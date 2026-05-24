// Package api implements the retrieval-service HTTP handlers.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/retrieval-service/internal/cache"
	"github.com/governance-platform/retrieval-service/internal/injection"
	"github.com/governance-platform/retrieval-service/internal/metrics"
	"github.com/governance-platform/retrieval-service/internal/retrieval"
	"github.com/governance-platform/retrieval-service/internal/router"
	"github.com/governance-platform/retrieval-service/internal/snapshot"
	"github.com/governance-platform/retrieval-service/internal/store"
)

var tracer = otel.Tracer("retrieval-service")

// Handler holds all dependencies for the HTTP API.
type Handler struct {
	store      *store.Store
	cache      *cache.Cache
	snapBuilder *snapshot.Builder
	retSvc     *retrieval.Service
	router     *router.Router
	audit      pkgaudit.Auditor
	log        zerolog.Logger
	flagFn     func() bool
}

func New(
	st *store.Store,
	ca *cache.Cache,
	sb *snapshot.Builder,
	rs *retrieval.Service,
	rt *router.Router,
	audit pkgaudit.Auditor,
	log zerolog.Logger,
	flagFn func() bool,
) *Handler {
	return &Handler{
		store:      st,
		cache:      ca,
		snapBuilder: sb,
		retSvc:     rs,
		router:     rt,
		audit:      audit,
		log:        log,
		flagFn:     flagFn,
	}
}

// Register wires all routes into mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/retrieval/snapshot", h.handleSnapshot)
	mux.HandleFunc("POST /v1/retrieval/docs", h.handleDocs)
	mux.HandleFunc("POST /v1/retrieval/explain", h.handleExplain)
	mux.HandleFunc("POST /v1/retrieval/route", h.handleRoute)
}

// ── Session extraction ────────────────────────────────────────────────────────

func sessionFromRequest(r *http.Request) store.SessionCtx {
	return store.SessionCtx{
		TenantID:  r.Header.Get("X-Tenant-ID"),
		UserID:    r.Header.Get("X-User-ID"),
		UserRoles: splitHeader(r.Header.Get("X-User-Roles"), ","),
		SubjectAttrs: map[string]string{
			"department": r.Header.Get("X-Subject-Department"),
		},
	}
}

// ── POST /v1/retrieval/snapshot ───────────────────────────────────────────────

type snapshotRequest struct {
	DataSourceID string `json:"data_source_id"`
}

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "retrieval.snapshot")
	defer span.End()

	if !h.flagFn() {
		writeError(w, "feature_disabled", http.StatusNotFound)
		return
	}

	var req snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DataSourceID == "" {
		writeError(w, "invalid_request: data_source_id required", http.StatusBadRequest)
		return
	}

	sess := sessionFromRequest(r)
	if sess.TenantID == "" || sess.UserID == "" {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	span.SetAttributes(
		attribute.String("tenant_id", sess.TenantID),
		attribute.String("data_source_id", req.DataSourceID),
	)

	start := time.Now()

	// Fetch schema/policy versions.
	ver, err := h.store.GetDataSourceVersion(ctx, sess, req.DataSourceID)
	if err != nil {
		h.log.Error().Err(err).Msg("get data source version")
		writeError(w, "internal_error", http.StatusInternalServerError)
		return
	}

	cacheKey := cache.SnapshotKey(sess.UserID, req.DataSourceID, ver.SchemaVersion, ver.PolicySetVersion)

	data, cacheHit, err := h.cache.GetOrBuildSnapshot(ctx, cacheKey, func() ([]byte, error) {
		snap, err := h.snapBuilder.Build(ctx, sess, req.DataSourceID, ver)
		if err != nil {
			return nil, fmt.Errorf("build snapshot: %w", err)
		}
		return json.Marshal(snap)
	})
	if err != nil {
		h.log.Error().Err(err).Msg("build snapshot")
		writeError(w, "internal_error", http.StatusInternalServerError)
		return
	}

	dur := time.Since(start).Seconds()
	hitStr := "false"
	if cacheHit {
		hitStr = "true"
	}
	metrics.SnapshotDuration.WithLabelValues(sess.TenantID, hitStr).Observe(dur)
	metrics.SnapshotSize.WithLabelValues(sess.TenantID).Observe(float64(len(data)))
	if cacheHit {
		metrics.CacheHits.WithLabelValues("snapshot").Inc()
	} else {
		metrics.CacheMisses.WithLabelValues("snapshot").Inc()
	}

	// Audit the snapshot access.
	_ = h.audit.SystemEvent(ctx, pkgaudit.SystemEventInput{
		Action:   "retrieval.snapshot",
		Outcome:  "success",
		TenantID: sess.TenantID,
		ActorID:  sess.UserID,
		Metadata: map[string]any{
			"data_source_id":    req.DataSourceID,
			"schema_version":    ver.SchemaVersion,
			"policy_set_version": ver.PolicySetVersion,
			"cache_hit":         cacheHit,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", hitStr)
	_, _ = w.Write(data)
}

// ── POST /v1/retrieval/docs ───────────────────────────────────────────────────

type docsRequest struct {
	Query         string   `json:"query"`
	TopK          int      `json:"top_k"`
	DataSourceIDs []string `json:"data_source_ids"`
	MinSimilarity float64  `json:"min_similarity"`
}

func (h *Handler) handleDocs(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "retrieval.docs")
	defer span.End()

	if !h.flagFn() {
		writeError(w, "feature_disabled", http.StatusNotFound)
		return
	}

	var req docsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		writeError(w, "invalid_request: query required", http.StatusBadRequest)
		return
	}
	if len(req.Query) > 4096 {
		writeError(w, "invalid_request: query too long", http.StatusBadRequest)
		return
	}

	sess := sessionFromRequest(r)
	if sess.TenantID == "" || sess.UserID == "" {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	span.SetAttributes(
		attribute.String("tenant_id", sess.TenantID),
		attribute.Int("top_k", req.TopK),
	)

	// Fetch policy set version for cache keying.
	ver, _ := h.store.GetDataSourceVersion(ctx, sess, "")

	start := time.Now()
	resp, err := h.retSvc.Retrieve(ctx, sess, retrieval.Request{
		Query:         req.Query,
		TopK:          req.TopK,
		DataSourceIDs: req.DataSourceIDs,
		MinSimilarity: req.MinSimilarity,
	}, ver.PolicySetVersion)
	if err != nil {
		h.log.Error().Err(err).Msg("doc retrieval")
		// Distinguish provider unavailable vs internal error.
		if isNoRoute(err) {
			writeError(w, "no_private_provider: restricted content cannot be routed", http.StatusServiceUnavailable)
			return
		}
		writeError(w, "internal_error", http.StatusInternalServerError)
		return
	}

	dur := time.Since(start).Seconds()
	metrics.DocsDuration.WithLabelValues(sess.TenantID, "false").Observe(dur)
	metrics.LLMRouteDecisions.WithLabelValues(
		resp.ProviderRoute.ProviderName,
		resp.ContentClassification,
		"ok",
	).Inc()

	// Record injection defense triggers.
	for _, chunk := range resp.Chunks {
		for _, t := range chunk.Triggers {
			metrics.InjectionTriggers.WithLabelValues(t, sess.TenantID).Inc()
		}
	}

	writeJSON(w, resp)
}

// ── POST /v1/retrieval/explain ────────────────────────────────────────────────

type explainRequest struct {
	DataSourceID string `json:"data_source_id"`
	Query        string `json:"query"`
}

type explainResponse struct {
	Snapshot     any           `json:"snapshot"`
	Docs         any           `json:"docs"`
	DefenseRules []string      `json:"defense_rules"`
	RouteDecision router.Route `json:"route_decision"`
}

func (h *Handler) handleExplain(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "retrieval.explain")
	defer span.End()

	if !h.flagFn() {
		writeError(w, "feature_disabled", http.StatusNotFound)
		return
	}

	// Explain requires admin-level role check (retrieval.explain permission).
	if r.Header.Get("X-User-Role-Admin") != "true" {
		writeError(w, "forbidden: retrieval.explain requires admin role", http.StatusForbidden)
		return
	}

	var req explainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	sess := sessionFromRequest(r)
	ver, _ := h.store.GetDataSourceVersion(ctx, sess, req.DataSourceID)

	snap, _ := h.snapBuilder.Build(ctx, sess, req.DataSourceID, ver)
	docs, _ := h.retSvc.Retrieve(ctx, sess, retrieval.Request{
		Query: req.Query,
		TopK:  5,
	}, ver.PolicySetVersion)

	var route router.Route
	if docs != nil {
		route = docs.ProviderRoute
	}

	writeJSON(w, explainResponse{
		Snapshot:      snap,
		Docs:          docs,
		DefenseRules:  injection.SystemPromptInstruction()[:100:100] + "...",
		RouteDecision: route,
	})
}

// ── POST /v1/retrieval/route ──────────────────────────────────────────────────

type routeRequest struct {
	ContentClassifications []string `json:"content_classifications"`
}

func (h *Handler) handleRoute(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "retrieval.route")
	defer span.End()

	if !h.flagFn() {
		writeError(w, "feature_disabled", http.StatusNotFound)
		return
	}

	var req routeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid_request", http.StatusBadRequest)
		return
	}

	sess := sessionFromRequest(r)
	maxClass := router.MaxClassification(req.ContentClassifications)

	span.SetAttributes(attribute.String("max_classification", maxClass))

	routes, _ := h.store.GetProviderRoutes(ctx, sess, maxClass)
	route, err := h.router.Decide(maxClass, routes)
	if err != nil {
		if isNoRoute(err) {
			metrics.LLMRouteDecisions.WithLabelValues("none", maxClass, "refused").Inc()
			writeError(w, "no_private_provider", http.StatusServiceUnavailable)
			return
		}
		writeError(w, "internal_error", http.StatusInternalServerError)
		return
	}

	metrics.LLMRouteDecisions.WithLabelValues(route.ProviderName, maxClass, "ok").Inc()
	writeJSON(w, route)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": msg})
}

func splitHeader(s, sep string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range splitOn(s, sep[0]) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitOn(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func isNoRoute(err error) bool {
	if err == nil {
		return false
	}
	return err.Error()[:min(len(err.Error()), 18)] == "no private provider"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
