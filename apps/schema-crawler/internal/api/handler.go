// Package api implements the HTTP API for the schema catalog service.
package api

import (
	"encoding/json"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"github.com/rs/zerolog"

	"github.com/governance-platform/schema-crawler/internal/scheduler"
	"github.com/governance-platform/schema-crawler/internal/store"
)

var tracer = otel.Tracer("schema-crawler/api")

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	db        *store.DB
	scheduler *scheduler.Scheduler
	log       zerolog.Logger
	flagOn    func() bool
}

// New returns an initialised Handler.
func New(db *store.DB, sched *scheduler.Scheduler, log zerolog.Logger, flagOn func() bool) *Handler {
	return &Handler{db: db, scheduler: sched, log: log, flagOn: flagOn}
}

// Register mounts all catalog routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/catalog/columns", h.listColumns)
	mux.HandleFunc("POST /api/v1/catalog/classify", h.classifyColumn)
	mux.HandleFunc("POST /api/v1/catalog/bulk-classify", h.bulkClassify)
	mux.HandleFunc("POST /api/v1/catalog/crawl", h.triggerCrawl)
}

func (h *Handler) featureCheck(w http.ResponseWriter) bool {
	if !h.flagOn() {
		writeError(w, http.StatusNotFound, "feature_disabled", "schema-catalog feature flag is off")
		return false
	}
	return true
}

// listColumns — GET /api/v1/catalog/columns?tenant_id=&data_source_id=&quarantine=
func (h *Handler) listColumns(w http.ResponseWriter, r *http.Request) {
	if !h.featureCheck(w) {
		return
	}
	ctx, span := tracer.Start(r.Context(), "api.listColumns")
	defer span.End()

	tenantID := r.URL.Query().Get("tenant_id")
	dataSourceID := r.URL.Query().Get("data_source_id")
	if tenantID == "" || dataSourceID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "tenant_id and data_source_id are required")
		return
	}
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	cols, err := h.db.ListColumns(ctx, tenantID, dataSourceID)
	if err != nil {
		h.log.Error().Err(err).Msg("list columns failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list columns")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": cols,
		"count": len(cols),
	})
}

type classifyRequest struct {
	TenantID       string   `json:"tenant_id"`
	ColumnID       string   `json:"column_id"`
	Classification string   `json:"classification"`
	Tags           []string `json:"tags"`
}

// classifyColumn — POST /api/v1/catalog/classify
func (h *Handler) classifyColumn(w http.ResponseWriter, r *http.Request) {
	if !h.featureCheck(w) {
		return
	}
	ctx, span := tracer.Start(r.Context(), "api.classifyColumn")
	defer span.End()

	var req classifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TenantID == "" || req.ColumnID == "" || req.Classification == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "tenant_id, column_id, classification are required")
		return
	}

	validClassifications := map[string]bool{"public": true, "internal": true, "confidential": true, "restricted": true}
	if !validClassifications[req.Classification] {
		writeError(w, http.StatusBadRequest, "invalid_classification", "must be one of: public, internal, confidential, restricted")
		return
	}

	if err := h.db.ClassifyColumn(ctx, req.TenantID, req.ColumnID, req.Classification, "steward", req.Tags); err != nil {
		h.log.Error().Err(err).Msg("classify column failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "classification failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type bulkClassifyRequest struct {
	TenantID       string   `json:"tenant_id"`
	ColumnIDs      []string `json:"column_ids"`
	Classification string   `json:"classification"`
	Tags           []string `json:"tags"`
}

// bulkClassify — POST /api/v1/catalog/bulk-classify
func (h *Handler) bulkClassify(w http.ResponseWriter, r *http.Request) {
	if !h.featureCheck(w) {
		return
	}
	ctx, span := tracer.Start(r.Context(), "api.bulkClassify")
	defer span.End()

	var req bulkClassifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TenantID == "" || len(req.ColumnIDs) == 0 || req.Classification == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "tenant_id, column_ids, classification are required")
		return
	}
	if len(req.ColumnIDs) > 10000 {
		writeError(w, http.StatusBadRequest, "too_many", "bulk classify is capped at 10000 columns per request")
		return
	}

	applied := 0
	for _, colID := range req.ColumnIDs {
		if err := h.db.ClassifyColumn(ctx, req.TenantID, colID, req.Classification, "steward", req.Tags); err != nil {
			h.log.Error().Err(err).Str("column_id", colID).Msg("bulk classify column failed")
			continue
		}
		applied++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"applied_count": applied,
		"total_count":   len(req.ColumnIDs),
	})
}

type crawlTriggerRequest struct {
	TenantID     string `json:"tenant_id"`
	DataSourceID string `json:"data_source_id"`
}

// triggerCrawl — POST /api/v1/catalog/crawl
func (h *Handler) triggerCrawl(w http.ResponseWriter, r *http.Request) {
	if !h.featureCheck(w) {
		return
	}
	_, span := tracer.Start(r.Context(), "api.triggerCrawl")
	defer span.End()

	var req crawlTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TenantID == "" || req.DataSourceID == "" {
		writeError(w, http.StatusBadRequest, "missing_param", "tenant_id and data_source_id are required")
		return
	}

	h.scheduler.Trigger(req.TenantID, req.DataSourceID)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "crawl enqueued"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": code, "message": msg})
}
