// Package siem exports audit events in CEF or JSON format for SIEM ingestion.
package siem

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/siem")

type Handler struct {
	db  *store.DB
	log *zap.Logger
}

func NewHandler(db *store.DB, log *zap.Logger) *Handler {
	return &Handler{db: db, log: log}
}

// Export handles GET /v1/admin/:tenant_id/audit/export/siem
// Query params: format=cef|json, from=RFC3339, to=RFC3339, limit=int
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "siem.Export")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "cef" && format != "json" {
		http.Error(w, `{"code":"bad_request","message":"format must be cef or json"}`, http.StatusBadRequest)
		return
	}

	from := time.Now().Add(-24 * time.Hour)
	to := time.Now()
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	limit := 10000
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscan(v, &limit)
		if limit > 50000 {
			limit = 50000
		}
	}

	rows, err := h.db.Pool.Query(ctx,
		`SELECT id, tenant_id, actor_id, action, resource_type, resource_id,
		        outcome, metadata, created_at
		   FROM policy_audit
		  WHERE tenant_id = $1
		    AND created_at BETWEEN $2 AND $3
		  ORDER BY created_at ASC
		  LIMIT $4`,
		tenantID, from, to, limit)
	if err != nil {
		h.log.Error("siem export query", zap.Error(err))
		http.Error(w, `{"code":"internal","message":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type AuditRow struct {
		ID           string          `json:"id"`
		TenantID     string          `json:"tenant_id"`
		ActorID      string          `json:"actor_id"`
		Action       string          `json:"action"`
		ResourceType string          `json:"resource_type"`
		ResourceID   string          `json:"resource_id"`
		Outcome      string          `json:"outcome"`
		Metadata     json.RawMessage `json:"metadata"`
		CreatedAt    time.Time       `json:"created_at"`
	}

	var events []AuditRow
	for rows.Next() {
		var ev AuditRow
		if err := rows.Scan(&ev.ID, &ev.TenantID, &ev.ActorID, &ev.Action,
			&ev.ResourceType, &ev.ResourceID, &ev.Outcome, &ev.Metadata, &ev.CreatedAt); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if events == nil {
		events = []AuditRow{}
	}

	switch format {
	case "cef":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=audit-%s-%s.cef", tenantID, time.Now().Format("20060102")))
		for _, ev := range events {
			// CEF:Version|Device Vendor|Device Product|Device Version|Signature ID|Name|Severity|Extension
			severity := "5"
			if ev.Outcome == "deny" {
				severity = "7"
			}
			line := fmt.Sprintf("CEF:0|Platform|GovernancePlatform|1.0|%s|%s %s|%s|rt=%s suser=%s dvc=%s outcome=%s tid=%s\n",
				ev.Action,
				ev.ResourceType, ev.ResourceID,
				severity,
				ev.CreatedAt.UTC().Format("Jan 02 2006 15:04:05"),
				ev.ActorID,
				ev.ResourceType,
				ev.Outcome,
				ev.TenantID,
			)
			w.Write([]byte(line)) //nolint
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=audit-%s-%s.json", tenantID, time.Now().Format("20060102")))
		enc := json.NewEncoder(w)
		for _, ev := range events {
			enc.Encode(ev) //nolint
		}
	}

	_ = strings.NewReader // suppress import
	h.log.Info("siem export", zap.String("tenant_id", tenantID), zap.String("format", format), zap.Int("count", len(events)))
}
