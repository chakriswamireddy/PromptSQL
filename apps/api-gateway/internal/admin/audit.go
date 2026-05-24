package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AuditHandler handles /v1/admin/{tenantSlug}/audit/* routes.
type AuditHandler struct {
	pool *pgxpool.Pool
}

func NewAuditHandler(pool *pgxpool.Pool) *AuditHandler {
	return &AuditHandler{pool: pool}
}

type policyAuditRow struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenantId"`
	PolicyID    string          `json:"policyId"`
	Action      string          `json:"action"`
	ActorID     string          `json:"actorId"`
	ActorEmail  string          `json:"actorEmail"`
	AfterState  json.RawMessage `json:"afterState,omitempty"`
	RequestID   string          `json:"requestId"`
	RowHash     string          `json:"rowHash"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type accessAuditRow struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	UserID       string     `json:"userId"`
	UserEmail    string     `json:"userEmail"`
	DataSourceID string     `json:"dataSourceId"`
	Resource     string     `json:"resource"`
	Action       string     `json:"action"`
	Decision     string     `json:"decision"`
	Reason       string     `json:"reason"`
	RowCount     *int       `json:"rowCount,omitempty"`
	DurationMs   int        `json:"durationMs"`
	RiskScore    *int       `json:"riskScore,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (h *AuditHandler) PolicyAudit(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.audit.policy")
	defer span.End()

	sess := SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	q := r.URL.Query()
	policyID := q.Get("policyId")
	cursor := q.Get("cursor")

	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", sess.TenantID)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	baseQ := `
		SELECT
			pa.id::text, pa.tenant_id::text, pa.policy_id::text,
			pa.action, pa.actor_id::text,
			COALESCE(u.email, 'unknown') AS actor_email,
			pa.after_state, COALESCE(pa.request_id::text,''),
			COALESCE(encode(pa.row_hash,'hex'),'') AS row_hash,
			pa.created_at
		FROM policy_audit pa
		LEFT JOIN users u ON u.id = pa.actor_id
		WHERE pa.tenant_id = $1`

	args := []interface{}{sess.TenantID.String()}
	idx := 2

	if policyID != "" {
		baseQ += fmt.Sprintf(" AND pa.policy_id = $%d", idx)
		args = append(args, policyID)
		idx++
	}
	if cursor != "" {
		baseQ += fmt.Sprintf(" AND pa.created_at < $%d", idx)
		args = append(args, cursor)
		idx++
	}
	baseQ += " ORDER BY pa.created_at DESC LIMIT 50"

	rows, err := conn.Query(ctx, baseQ, args...)
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer rows.Close()

	var items []policyAuditRow
	for rows.Next() {
		var row policyAuditRow
		if err := rows.Scan(
			&row.ID, &row.TenantID, &row.PolicyID,
			&row.Action, &row.ActorID, &row.ActorEmail,
			&row.AfterState, &row.RequestID,
			&row.RowHash, &row.CreatedAt,
		); err != nil {
			continue
		}
		items = append(items, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

func (h *AuditHandler) AccessAudit(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.audit.access")
	defer span.End()

	sess := SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	q := r.URL.Query()
	decision := q.Get("decision")
	cursor := q.Get("cursor")

	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", sess.TenantID)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	baseQ := `
		SELECT
			aa.id::text, aa.tenant_id::text, aa.user_id::text,
			COALESCE(u.email,'unknown') AS user_email,
			aa.data_source_id::text, aa.resource, aa.action,
			aa.decision, aa.reason,
			aa.row_count, aa.duration_ms, aa.risk_score,
			aa.created_at
		FROM access_audit aa
		LEFT JOIN users u ON u.id = aa.user_id
		WHERE aa.tenant_id = $1`

	args := []interface{}{sess.TenantID.String()}
	idx := 2

	if decision != "" {
		baseQ += fmt.Sprintf(" AND aa.decision = $%d", idx)
		args = append(args, decision)
		idx++
	}
	if cursor != "" {
		baseQ += fmt.Sprintf(" AND aa.created_at < $%d", idx)
		args = append(args, cursor)
		idx++
	}
	baseQ += " ORDER BY aa.created_at DESC LIMIT 100"

	rows, err := conn.Query(ctx, baseQ, args...)
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer rows.Close()

	var items []accessAuditRow
	for rows.Next() {
		var row accessAuditRow
		if err := rows.Scan(
			&row.ID, &row.TenantID, &row.UserID,
			&row.UserEmail, &row.DataSourceID, &row.Resource, &row.Action,
			&row.Decision, &row.Reason,
			&row.RowCount, &row.DurationMs, &row.RiskScore,
			&row.CreatedAt,
		); err != nil {
			continue
		}
		items = append(items, row)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
