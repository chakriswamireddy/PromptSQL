// Package api implements the risk management REST endpoints.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("anomaly-detector-api")

// Handler holds dependencies for all risk API handlers.
type Handler struct {
	pool  *pgxpool.Pool
	rdb   *redis.Client
	log   zerolog.Logger
}

// NewHandler creates a Handler.
func NewHandler(pool *pgxpool.Pool, rdb *redis.Client, log zerolog.Logger) *Handler {
	return &Handler{pool: pool, rdb: rdb, log: log}
}

// GetRiskScore handles GET /v1/users/{userID}/risk-score.
// Reads the hot score from Redis; falls back to latest DB row.
func (h *Handler) GetRiskScore(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "api.GetRiskScore")
	defer span.End()

	tenantID := r.Header.Get("X-Tenant-ID")
	userID := r.PathValue("userID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "missing tenant or user ID")
		return
	}
	span.SetAttributes(attribute.String("tenant_id", tenantID), attribute.String("user_id", userID))

	key := fmt.Sprintf("risk:score:%s:%s", tenantID, userID)
	raw, err := h.rdb.Get(ctx, key).Bytes()
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Risk-Source", "redis")
		_, _ = w.Write(raw)
		return
	}

	// Fallback: latest score from PostgreSQL.
	row := h.dbLatestScore(ctx, tenantID, userID)
	if row == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id": tenantID,
			"user_id":   userID,
			"score":     0,
			"status":    "unknown",
		})
		return
	}
	w.Header().Set("X-Risk-Source", "db")
	writeJSON(w, http.StatusOK, row)
}

// GetRiskEvents handles GET /v1/risk/events.
func (h *Handler) GetRiskEvents(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "api.GetRiskEvents")
	defer span.End()

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Tenant-ID header")
		return
	}
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	const q = `
		SELECT id, user_id, kind, score_before, score_after, payload, created_at
		FROM risk_events
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT 100`

	rows, err := h.pool.Query(ctx, q, tenantID)
	if err != nil {
		h.log.Error().Err(err).Msg("query risk_events")
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()

	type riskEventRow struct {
		ID          string    `json:"id"`
		UserID      string    `json:"user_id"`
		Kind        string    `json:"kind"`
		ScoreBefore *int      `json:"score_before,omitempty"`
		ScoreAfter  *int      `json:"score_after,omitempty"`
		Payload     any       `json:"payload"`
		CreatedAt   time.Time `json:"created_at"`
	}

	events := make([]riskEventRow, 0, 100)
	for rows.Next() {
		var ev riskEventRow
		var payloadJSON []byte
		if err := rows.Scan(&ev.ID, &ev.UserID, &ev.Kind, &ev.ScoreBefore, &ev.ScoreAfter, &payloadJSON, &ev.CreatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal(payloadJSON, &ev.Payload)
		events = append(events, ev)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": events})
}

// PostRiskOverride handles POST /v1/users/{userID}/risk-override.
// Allows a trusted admin to manually set a user's risk score (audited).
func (h *Handler) PostRiskOverride(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "api.PostRiskOverride")
	defer span.End()

	tenantID := r.Header.Get("X-Tenant-ID")
	actorID := r.Header.Get("X-User-ID")
	userID := r.PathValue("userID")
	if tenantID == "" || userID == "" || actorID == "" {
		writeError(w, http.StatusBadRequest, "missing required headers or path params")
		return
	}
	span.SetAttributes(
		attribute.String("tenant_id", tenantID),
		attribute.String("user_id", userID),
		attribute.String("actor_id", actorID),
	)

	var body struct {
		Score  int    `json:"score"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Score < 0 || body.Score > 100 {
		writeError(w, http.StatusBadRequest, "score must be 0–100")
		return
	}
	if body.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required")
		return
	}

	// Record the override event.
	const q = `
		INSERT INTO risk_events (tenant_id, user_id, kind, score_after, payload, actor_id)
		VALUES ($1, $2, 'override', $3, $4, $5)`
	payload, _ := json.Marshal(map[string]string{"reason": body.Reason})
	if _, err := h.pool.Exec(ctx, q, tenantID, userID, body.Score, payload, actorID); err != nil {
		h.log.Error().Err(err).Msg("insert risk_events override")
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":   tenantID,
		"user_id":     userID,
		"score":       body.Score,
		"overridden_by": actorID,
	})
}

// GetCalibration handles GET /v1/risk/calibration.
func (h *Handler) GetCalibration(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "api.GetCalibration")
	defer span.End()

	tenantID := r.Header.Get("X-Tenant-ID")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "missing X-Tenant-ID")
		return
	}
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	const q = `
		SELECT id, weights, thresholds, version, created_at
		FROM risk_calibrations
		WHERE tenant_id = $1
		ORDER BY version DESC
		LIMIT 1`

	var (
		id, version         string
		weightsJSON, threshJSON []byte
		createdAt           time.Time
	)
	err := h.pool.QueryRow(ctx, q, tenantID).Scan(&id, &weightsJSON, &threshJSON, &version, &createdAt)
	if err != nil {
		// Return defaults if no calibration exists.
		writeJSON(w, http.StatusOK, map[string]any{
			"weights":    map[string]float64{"time_of_day": 0.25, "day_of_week": 0.10, "resource_novelty": 0.25, "row_volume": 0.20, "ip_drift": 0.20},
			"thresholds": map[string]int{"low": 40, "medium": 70, "high": 85},
		})
		return
	}

	var weights, thresholds any
	_ = json.Unmarshal(weightsJSON, &weights)
	_ = json.Unmarshal(threshJSON, &thresholds)

	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "weights": weights, "thresholds": thresholds,
		"version": version, "created_at": createdAt,
	})
}

// PutCalibration handles PUT /v1/risk/calibration.
func (h *Handler) PutCalibration(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "api.PutCalibration")
	defer span.End()

	tenantID := r.Header.Get("X-Tenant-ID")
	actorID := r.Header.Get("X-User-ID")
	if tenantID == "" || actorID == "" {
		writeError(w, http.StatusBadRequest, "missing required headers")
		return
	}
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	var body struct {
		Weights    map[string]float64 `json:"weights"`
		Thresholds map[string]int     `json:"thresholds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Validate weights sum to 1.0 (±0.01 tolerance).
	sum := 0.0
	for _, w := range body.Weights {
		sum += w
	}
	if sum < 0.99 || sum > 1.01 {
		writeError(w, http.StatusBadRequest, "weights must sum to 1.0")
		return
	}

	weightsJSON, _ := json.Marshal(body.Weights)
	threshJSON, _ := json.Marshal(body.Thresholds)

	const q = `
		INSERT INTO risk_calibrations (tenant_id, weights, thresholds, version, created_by)
		SELECT $1, $2, $3,
		       COALESCE((SELECT MAX(version)+1 FROM risk_calibrations WHERE tenant_id=$1), 1),
		       $4
		RETURNING id, version`

	var id string
	var version int
	if err := h.pool.QueryRow(ctx, q, tenantID, weightsJSON, threshJSON, actorID).Scan(&id, &version); err != nil {
		h.log.Error().Err(err).Msg("insert risk_calibrations")
		writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "version": version})
}

// dbLatestScore queries the most recent stored score from risk_scores.
func (h *Handler) dbLatestScore(ctx context.Context, tenantID, userID string) map[string]any {
	const q = `
		SELECT score, components, decayed_total, model_version, computed_at
		FROM risk_scores
		WHERE tenant_id = $1 AND user_id = $2
		ORDER BY computed_at DESC
		LIMIT 1`

	var (
		score, decayedTotal int
		modelVersion        string
		componentsJSON      []byte
		computedAt          time.Time
	)
	err := h.pool.QueryRow(ctx, q, tenantID, userID).Scan(
		&score, &componentsJSON, &decayedTotal, &modelVersion, &computedAt,
	)
	if err != nil {
		return nil
	}

	var components any
	_ = json.Unmarshal(componentsJSON, &components)

	return map[string]any{
		"tenant_id":     tenantID,
		"user_id":       userID,
		"score":         score,
		"decayed_total": decayedTotal,
		"model_version": modelVersion,
		"computed_at":   computedAt,
		"components":    components,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"code": msg})
}
