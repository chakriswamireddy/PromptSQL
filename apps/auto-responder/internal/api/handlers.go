// Package api implements the auto-responder HTTP handlers.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/governance-platform/auto-responder/internal/breakglass"
	"github.com/governance-platform/auto-responder/internal/playbook"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/pkg/obligation"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const tracerName = "auto-responder"

// Handler holds all dependencies for the HTTP layer.
type Handler struct {
	bg        *breakglass.Store
	playbooks *playbook.Store
	obSvc     *obligation.Service
	log       logging.Logger
}

// New creates an Handler.
func New(
	bg *breakglass.Store,
	pbs *playbook.Store,
	obSvc *obligation.Service,
	log logging.Logger,
) *Handler {
	return &Handler{bg: bg, playbooks: pbs, obSvc: obSvc, log: log}
}

// RegisterRoutes registers all auto-responder routes on mux.
// All routes are prefixed with /v1/admin/{tenantSlug} and require the caller
// to have already been authenticated by the api-gateway middleware.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Break-glass
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/breakglass/request",        h.RequestBreakGlass)
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/breakglass/{id}/approve",   h.ApproveBreakGlass)
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/breakglass/{id}/terminate", h.TerminateBreakGlass)
	mux.HandleFunc("GET  /v1/admin/{tenantSlug}/breakglass/sessions",       h.ListBreakGlass)
	mux.HandleFunc("GET  /v1/admin/{tenantSlug}/breakglass/sessions/{id}",  h.GetBreakGlass)

	// Playbooks
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/playbooks",              h.CreatePlaybook)
	mux.HandleFunc("GET  /v1/admin/{tenantSlug}/playbooks",              h.ListPlaybooks)
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/playbooks/{id}/activate", h.ActivatePlaybook)
	mux.HandleFunc("POST /v1/admin/{tenantSlug}/playbooks/simulate",     h.SimulatePlaybook)

	// Step-up
	mux.HandleFunc("POST /v1/auth/step-up/initiate",  h.StepUpInitiate)
	mux.HandleFunc("POST /v1/auth/step-up/complete",  h.StepUpComplete)

	// Health
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz",  h.Readiness)
}

// ── Break-Glass ────────────────────────────────────────────────────────────────

func (h *Handler) RequestBreakGlass(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer(tracerName).Start(r.Context(), "breakglass.request")
	defer span.End()

	tenantID := tenantFromRequest(r)
	actorID := actorFromHeader(r)
	if tenantID == "" || actorID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}

	var body struct {
		PrincipalID    string          `json:"principal_id"`
		Scope          json.RawMessage `json:"scope"`
		Reason         string          `json:"reason"`
		MaxDurationSec int             `json:"max_duration_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if body.MaxDurationSec <= 0 || body.MaxDurationSec > 3600 {
		body.MaxDurationSec = 3600
	}

	sess := &breakglass.Session{
		TenantID:       tenantID,
		PrincipalID:    body.PrincipalID,
		InitiatorID:    actorID,
		Scope:          body.Scope,
		Reason:         body.Reason,
		MaxDurationSec: body.MaxDurationSec,
	}
	id, err := h.bg.Create(ctx, sess)
	if err != nil {
		h.log.Error().Err(err).Msg("breakglass create failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	_ = h.bg.AppendAudit(ctx, tenantID, id, actorID, "request", map[string]any{
		"reason":       body.Reason,
		"principal_id": body.PrincipalID,
	})

	span.SetAttributes(attribute.String("breakglass.session_id", id))
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "pending_approval"})
}

func (h *Handler) ApproveBreakGlass(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer(tracerName).Start(r.Context(), "breakglass.approve")
	defer span.End()

	tenantID := tenantFromRequest(r)
	actorID := actorFromHeader(r)
	sessionID := r.PathValue("id")

	sess, err := h.bg.Approve(ctx, tenantID, sessionID, actorID)
	if errors.Is(err, breakglass.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if errors.Is(err, breakglass.ErrInitiatorCannotApprove) {
		writeError(w, http.StatusConflict, "initiator_cannot_approve", "")
		return
	}
	if errors.Is(err, breakglass.ErrAlreadyApproved) {
		writeError(w, http.StatusConflict, "already_approved", "")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Msg("breakglass approve failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	_ = h.bg.AppendAudit(ctx, tenantID, sessionID, actorID, "approve", map[string]any{
		"new_status": sess.Status,
	})
	span.SetAttributes(
		attribute.String("breakglass.session_id", sessionID),
		attribute.String("breakglass.new_status", sess.Status),
	)
	writeJSON(w, http.StatusOK, sess)
}

func (h *Handler) TerminateBreakGlass(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer(tracerName).Start(r.Context(), "breakglass.terminate")
	defer span.End()

	tenantID := tenantFromRequest(r)
	actorID := actorFromHeader(r)
	sessionID := r.PathValue("id")

	sess, err := h.bg.Terminate(ctx, tenantID, sessionID, actorID)
	if errors.Is(err, breakglass.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	_ = h.bg.AppendAudit(ctx, tenantID, sessionID, actorID, "terminate", nil)
	span.SetAttributes(attribute.String("breakglass.session_id", sessionID))
	writeJSON(w, http.StatusOK, sess)
}

func (h *Handler) ListBreakGlass(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)
	sessions, err := h.bg.ListActive(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if sessions == nil {
		sessions = []*breakglass.Session{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": sessions})
}

func (h *Handler) GetBreakGlass(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)
	sess, err := h.bg.Get(ctx, tenantID, r.PathValue("id"))
	if errors.Is(err, breakglass.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

// ── Playbooks ──────────────────────────────────────────────────────────────────

func (h *Handler) CreatePlaybook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)
	actorID := actorFromHeader(r)

	var body struct {
		Tiers              json.RawMessage `json:"tiers"`
		EscalationTargets  json.RawMessage `json:"escalation_targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	id, err := h.playbooks.Create(ctx, tenantID, actorID, body.Tiers, body.EscalationTargets)
	if err != nil {
		h.log.Error().Err(err).Msg("playbook create failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *Handler) ListPlaybooks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)
	pbs, err := h.playbooks.List(ctx, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": pbs})
}

func (h *Handler) ActivatePlaybook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)
	actorID := actorFromHeader(r)
	pbID := r.PathValue("id")

	if err := h.playbooks.Activate(ctx, tenantID, pbID, actorID); err != nil {
		if errors.Is(err, playbook.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "activated"})
}

func (h *Handler) SimulatePlaybook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := tenantFromRequest(r)

	var body struct {
		Score     int    `json:"score"`
		PlaybookID string `json:"playbook_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	var pb *playbook.Playbook
	var err error
	if body.PlaybookID != "" {
		pb, err = h.playbooks.GetByID(ctx, tenantID, body.PlaybookID)
	} else {
		pb, err = h.playbooks.GetActive(ctx, tenantID)
	}
	if err != nil {
		pb = playbook.DefaultPlaybook
	}

	result := pb.Evaluate(body.Score)
	writeJSON(w, http.StatusOK, map[string]any{
		"score":  body.Score,
		"action": result.Action,
		"tier":   result.Tier,
		"params": result.Params,
	})
}

// ── Step-Up ────────────────────────────────────────────────────────────────────

func (h *Handler) StepUpInitiate(w http.ResponseWriter, r *http.Request) {
	if h.obSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "obligation_service_unavailable", "")
		return
	}
	tenantID := r.Header.Get("X-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	jti := r.Header.Get("X-Session-JTI")
	riskScoreStr := r.Header.Get("X-Risk-Score")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}

	var riskScore int
	if riskScoreStr != "" {
		_, _ = riskScoreStr, &riskScore // parsed via format verbs
		_ = json.Unmarshal([]byte(riskScoreStr), &riskScore)
	}

	tokenStr, tok, err := h.obSvc.Issue(tenantID, userID, jti, "risk_threshold", riskScore)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue_failed", "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"obligation_token": tokenStr,
		"expires_at":       tok.ExpiresAt.Format(time.RFC3339),
		"ttl_sec":          300,
		"type":             "require_mfa",
	})
}

func (h *Handler) StepUpComplete(w http.ResponseWriter, r *http.Request) {
	if h.obSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "obligation_service_unavailable", "")
		return
	}
	var body struct {
		ObligationToken string `json:"obligation_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	tok, err := h.obSvc.Verify(body.ObligationToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_obligation_token", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"satisfied": true,
		"token_id":  tok.ID,
		"user_id":   tok.UserID,
	})
}

// ── Health ─────────────────────────────────────────────────────────────────────

func (h *Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) Readiness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func tenantFromRequest(r *http.Request) string {
	if slug := r.PathValue("tenantSlug"); slug != "" {
		return slug
	}
	return r.Header.Get("X-Tenant-ID")
}

func actorFromHeader(r *http.Request) string {
	return r.Header.Get("X-User-ID")
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, errCode, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := map[string]string{"code": errCode}
	if detail != "" {
		resp["detail"] = detail
	}
	_ = json.NewEncoder(w).Encode(resp)
}
