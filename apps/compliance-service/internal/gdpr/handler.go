// Package gdpr handles GDPR Subject Access Requests (Art. 15, 17, 20).
package gdpr

import (
	"encoding/json"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/gdpr")

type Handler struct {
	db  *store.DB
	log *zap.Logger
}

func NewHandler(db *store.DB, log *zap.Logger) *Handler {
	return &Handler{db: db, log: log}
}

// Submit handles POST /v1/admin/:tenant_id/gdpr/requests
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "gdpr.Submit")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	var body struct {
		SubjectEmail string `json:"subject_email"`
		RequestType  string `json:"request_type"`
		Notes        string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"code":"bad_request","message":"invalid body"}`, http.StatusBadRequest)
		return
	}
	validTypes := map[string]bool{"access": true, "erasure": true, "portability": true, "rectification": true}
	if !validTypes[body.RequestType] {
		http.Error(w, `{"code":"bad_request","message":"invalid request_type"}`, http.StatusBadRequest)
		return
	}

	var requestID string
	err := h.db.Pool.QueryRow(ctx,
		`INSERT INTO gdpr_sar_requests (tenant_id, subject_email, request_type, notes)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		tenantID, body.SubjectEmail, body.RequestType, body.Notes).Scan(&requestID)
	if err != nil {
		h.log.Error("gdpr submit", zap.Error(err))
		http.Error(w, `{"code":"internal","message":"insert failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"request_id": requestID}) //nolint
}

// List handles GET /v1/admin/:tenant_id/gdpr/requests
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "gdpr.List")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	rows, err := h.db.Pool.Query(ctx,
		`SELECT id, subject_email, request_type, status, submitted_at, due_at, completed_at
		   FROM gdpr_sar_requests WHERE tenant_id=$1 ORDER BY submitted_at DESC`,
		tenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Row struct {
		ID           string     `json:"id"`
		SubjectEmail string     `json:"subject_email"`
		RequestType  string     `json:"request_type"`
		Status       string     `json:"status"`
		SubmittedAt  time.Time  `json:"submitted_at"`
		DueAt        time.Time  `json:"due_at"`
		CompletedAt  *time.Time `json:"completed_at,omitempty"`
	}
	var requests []Row
	for rows.Next() {
		var req Row
		rows.Scan(&req.ID, &req.SubjectEmail, &req.RequestType, &req.Status, &req.SubmittedAt, &req.DueAt, &req.CompletedAt) //nolint
		requests = append(requests, req)
	}
	if requests == nil {
		requests = []Row{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"requests": requests}) //nolint
}

// UpdateStatus handles PUT /v1/admin/:tenant_id/gdpr/requests/:request_id/status
func (h *Handler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "gdpr.UpdateStatus")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	requestID := r.PathValue("request_id")

	var body struct {
		Status      string  `json:"status"`
		ProcessedBy string  `json:"processed_by"`
		DownloadURL *string `json:"download_url,omitempty"`
		Notes       *string `json:"notes,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"code":"bad_request","message":"invalid body"}`, http.StatusBadRequest)
		return
	}

	var completedAt interface{}
	if body.Status == "completed" {
		completedAt = time.Now()
	}

	_, err := h.db.Pool.Exec(ctx,
		`UPDATE gdpr_sar_requests
		    SET status=$1, processed_by=$2, download_url=$3, notes=$4, completed_at=$5
		  WHERE id=$6 AND tenant_id=$7`,
		body.Status, body.ProcessedBy, body.DownloadURL, body.Notes, completedAt, requestID, tenantID)
	if err != nil {
		h.log.Error("gdpr update status", zap.Error(err))
		http.Error(w, `{"code":"internal","message":"update failed"}`, http.StatusInternalServerError)
		return
	}

	span.SetAttributes(attribute.String("status", body.Status))
	w.WriteHeader(http.StatusNoContent)
}
