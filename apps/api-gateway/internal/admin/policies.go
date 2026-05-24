package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	pdpv1 "github.com/governance-platform/pkg/pdpv1"
)

var tracer = otel.Tracer("api-gateway/admin")

const maxPolicyBodyBytes = 256 * 1024 // 256 KB

// PoliciesHandler handles all /v1/admin/{tenantSlug}/policies/* routes.
type PoliciesHandler struct {
	pool    *pgxpool.Pool
	pdpConn pdpv1.PDPClient // gRPC client to PDP for validate/explain
}

func NewPoliciesHandler(pool *pgxpool.Pool, pdpConn pdpv1.PDPClient) *PoliciesHandler {
	return &PoliciesHandler{pool: pool, pdpConn: pdpConn}
}

// List handles GET /v1/admin/{tenantSlug}/policies
func (h *PoliciesHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.list")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	q := r.URL.Query()
	status := q.Get("status")
	action := q.Get("action")
	cursor := q.Get("cursor")
	limit := 50

	rows, err := listPolicies(ctx, h.pool, sess.TenantID, status, action, cursor, limit)
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", "failed to list policies"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":      rows,
		"total":      len(rows),
		"nextCursor": nextCursorFromRows(rows, limit),
	})
}

// Get handles GET /v1/admin/{tenantSlug}/policies/{id}
func (h *PoliciesHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.get")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	id := r.PathValue("id")
	policy, err := getPolicy(ctx, h.pool, sess.TenantID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, errBody("not_found", "policy not found"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	w.Header().Set("ETag", `"`+policy.ETag+`"`)
	writeJSON(w, http.StatusOK, policy)
}

// Create handles POST /v1/admin/{tenantSlug}/policies
func (h *PoliciesHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.create")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	body, err := readBody(r, maxPolicyBodyBytes)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("body_too_large", "policy body must be ≤ 256 KB"))
		return
	}

	var draft map[string]interface{}
	if err := json.Unmarshal(body, &draft); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_json", err.Error()))
		return
	}

	name, _ := draft["name"].(string)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errBody("validation_error", "name is required"))
		return
	}

	policyID := uuid.New()
	etag := sha256Hex(body)

	condJSON, _ := jsonMarshal(draft["conditions"])
	obligJSON, _ := jsonMarshal(draft["obligations"])
	filterJSON, _ := jsonMarshal(draft["rowFilter"])
	subjectJSON, _ := jsonMarshalDefault(draft["subjectMatch"], `{}`)
	resourceJSON, _ := jsonMarshalDefault(draft["resourceMatch"], `{}`)
	masksJSON, _ := jsonMarshal(draft["columnMasks"])
	allowedCols := toStringSlice(draft["allowedColumns"])
	deniedCols := toStringSlice(draft["deniedColumns"])

	effect := "allow"
	if e, ok := draft["effect"].(string); ok && (e == "allow" || e == "deny") {
		effect = e
	}
	action := "*"
	if a, ok := draft["action"].(string); ok && a != "" {
		action = a
	}

	err = withTxSession(ctx, h.pool, sess, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO policies (
				id, tenant_id, name, version, status, effect,
				subject_match, resource_match, action,
				conditions, obligations, allowed_columns, denied_columns,
				row_filter, column_masks, created_by, etag
			) VALUES (
				$1, $2, $3, 1, 'draft', $4,
				$5, $6, $7,
				$8, $9, $10, $11,
				$12, $13, $14, $15
			)`,
			policyID, sess.TenantID, name, effect,
			subjectJSON, resourceJSON, action,
			condJSON, obligJSON, allowedCols, deniedCols,
			filterJSON, masksJSON, sess.UserID, etag,
		)
		return err
	})
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	policy, _ := getPolicy(ctx, h.pool, sess.TenantID, policyID.String())
	w.Header().Set("ETag", `"`+etag+`"`)
	writeJSON(w, http.StatusCreated, policy)
}

// Update handles PUT /v1/admin/{tenantSlug}/policies/{id}
func (h *PoliciesHandler) Update(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.update")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	id := r.PathValue("id")

	// Optimistic concurrency via If-Match / ETag.
	ifMatch := r.Header.Get("If-Match")

	body, err := readBody(r, maxPolicyBodyBytes)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errBody("body_too_large", "policy body must be ≤ 256 KB"))
		return
	}

	var draft map[string]interface{}
	if err := json.Unmarshal(body, &draft); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_json", err.Error()))
		return
	}

	newETag := sha256Hex(body)

	condJSON, _ := jsonMarshal(draft["conditions"])
	obligJSON, _ := jsonMarshal(draft["obligations"])
	filterJSON, _ := jsonMarshal(draft["rowFilter"])
	subjectJSON, _ := jsonMarshalDefault(draft["subjectMatch"], `{}`)
	resourceJSON, _ := jsonMarshalDefault(draft["resourceMatch"], `{}`)
	masksJSON, _ := jsonMarshal(draft["columnMasks"])
	allowedCols := toStringSlice(draft["allowedColumns"])
	deniedCols := toStringSlice(draft["deniedColumns"])

	var updated bool
	err = withTxSession(ctx, h.pool, sess, func(ctx context.Context, tx pgx.Tx) error {
		cond := ""
		args := []interface{}{
			subjectJSON, resourceJSON, condJSON, obligJSON,
			allowedCols, deniedCols, filterJSON, masksJSON,
			newETag, id, sess.TenantID,
		}
		if ifMatch != "" {
			// Strip surrounding quotes from ETag header.
			etag := stripQuotes(ifMatch)
			cond = "AND etag = $12"
			args = append(args, etag)
		}

		tag, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE policies SET
				subject_match = $1, resource_match = $2,
				conditions = $3, obligations = $4,
				allowed_columns = $5, denied_columns = $6,
				row_filter = $7, column_masks = $8,
				etag = $9, updated_at = now()
			WHERE id = $10 AND tenant_id = $11 AND status = 'draft'
			%s`, cond),
			args...,
		)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	if !updated {
		if ifMatch != "" {
			writeJSON(w, http.StatusConflict, errBody("conflict", "ETag mismatch — reload and reapply"))
		} else {
			writeJSON(w, http.StatusNotFound, errBody("not_found", "policy not found or not in draft status"))
		}
		return
	}

	policy, _ := getPolicy(ctx, h.pool, sess.TenantID, id)
	w.Header().Set("ETag", `"`+newETag+`"`)
	writeJSON(w, http.StatusOK, policy)
}

// Submit handles POST /v1/admin/{tenantSlug}/policies/{id}/submit
func (h *PoliciesHandler) Submit(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.submit")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	id := r.PathValue("id")

	var updated bool
	err := withTxSession(ctx, h.pool, sess, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE policies SET
				status = 'pending_review',
				submitted_by = $1, submitted_at = now(),
				updated_at = now()
			WHERE id = $2 AND tenant_id = $3 AND status = 'draft'`,
			sess.UserID, id, sess.TenantID,
		)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		if !updated {
			return nil
		}
		return writePolicyAudit(ctx, tx, sess, id, "policy.submitted", nil)
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, errBody("not_found", "policy not found or not in draft status"))
		return
	}

	policy, _ := getPolicy(ctx, h.pool, sess.TenantID, id)
	writeJSON(w, http.StatusOK, policy)
}

// Approve handles POST /v1/admin/{tenantSlug}/policies/{id}/approve
// Atomically activates the policy, archives the previous active version,
// and writes the outbox event for pub/sub relay.
func (h *PoliciesHandler) Approve(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.approve")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	id := r.PathValue("id")

	var conflictMsg string
	err := withTxSession(ctx, h.pool, sess, func(ctx context.Context, tx pgx.Tx) error {
		// Load the draft to approve.
		var policyName string
		var submittedBy *string
		err := tx.QueryRow(ctx,
			`SELECT name, submitted_by FROM policies WHERE id = $1 AND tenant_id = $2 AND status = 'pending_review'`,
			id, sess.TenantID,
		).Scan(&policyName, &submittedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			conflictMsg = "policy not found or not pending review"
			return nil
		}
		if err != nil {
			return err
		}

		// Dual-approval: approver must differ from submitter when configured.
		if submittedBy != nil && *submittedBy == sess.UserID.String() {
			dualRequired := false
			_ = tx.QueryRow(ctx,
				`SELECT COALESCE((config->>'dual_approval')::boolean, false)
				 FROM tenants WHERE id = $1`, sess.TenantID,
			).Scan(&dualRequired)
			if dualRequired {
				conflictMsg = "dual_approval_required: approver must differ from submitter"
				return nil
			}
		}

		// Archive previous active version of same policy name.
		_, err = tx.Exec(ctx, `
			UPDATE policies SET status = 'archived', updated_at = now()
			WHERE tenant_id = $1 AND name = $2 AND status = 'active'`,
			sess.TenantID, policyName,
		)
		if err != nil {
			return err
		}

		// Activate the new version.
		_, err = tx.Exec(ctx, `
			UPDATE policies SET
				status = 'active', approved_by = $1, updated_at = now()
			WHERE id = $2 AND tenant_id = $3`,
			sess.UserID, id, sess.TenantID,
		)
		if err != nil {
			return err
		}

		// Write audit row.
		if err := writePolicyAudit(ctx, tx, sess, id, "policy.activated", nil); err != nil {
			return err
		}

		// Outbox event for pub/sub relay → PDP cache invalidation.
		_, err = tx.Exec(ctx, `
			INSERT INTO outbox_events (tenant_id, kind, payload)
			VALUES ($1, 'policy.activated', $2)`,
			sess.TenantID,
			fmt.Sprintf(`{"policyId":"%s","tenantId":"%s"}`, id, sess.TenantID),
		)
		return err
	})
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	if conflictMsg != "" {
		status := http.StatusConflict
		if conflictMsg == "policy not found or not pending review" {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errBody("conflict", conflictMsg))
		return
	}

	policy, _ := getPolicy(ctx, h.pool, sess.TenantID, id)
	writeJSON(w, http.StatusOK, policy)
}

// Archive handles POST /v1/admin/{tenantSlug}/policies/{id}/archive
func (h *PoliciesHandler) Archive(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.policies.archive")
	defer span.End()

	sess := auth.SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	id := r.PathValue("id")
	var updated bool

	err := withTxSession(ctx, h.pool, sess, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE policies SET status = 'archived', updated_at = now()
			WHERE id = $1 AND tenant_id = $2
			AND status IN ('active','pending_review')`,
			id, sess.TenantID,
		)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() > 0
		if !updated {
			return nil
		}
		if err := writePolicyAudit(ctx, tx, sess, id, "policy.archived", nil); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO outbox_events (tenant_id, kind, payload)
			VALUES ($1, 'policy.archived', $2)`,
			sess.TenantID,
			fmt.Sprintf(`{"policyId":"%s","tenantId":"%s"}`, id, sess.TenantID),
		)
		return err
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	if !updated {
		writeJSON(w, http.StatusNotFound, errBody("not_found", "policy not found"))
		return
	}

	policy, _ := getPolicy(ctx, h.pool, sess.TenantID, id)
	writeJSON(w, http.StatusOK, policy)
}

// ── DB helpers ───────────────────────────────────────────────────────────────

type policyRow struct {
	ID             string                 `json:"id"`
	TenantID       string                 `json:"tenantId"`
	Name           string                 `json:"name"`
	Version        int                    `json:"version"`
	Status         string                 `json:"status"`
	Effect         string                 `json:"effect"`
	SubjectMatch   map[string]interface{} `json:"subjectMatch"`
	ResourceMatch  map[string]interface{} `json:"resourceMatch"`
	Action         string                 `json:"action"`
	Conditions     interface{}            `json:"conditions,omitempty"`
	Obligations    interface{}            `json:"obligations,omitempty"`
	AllowedColumns []string               `json:"allowedColumns,omitempty"`
	DeniedColumns  []string               `json:"deniedColumns,omitempty"`
	RowFilter      interface{}            `json:"rowFilter,omitempty"`
	ColumnMasks    map[string]string      `json:"columnMasks,omitempty"`
	CreatedBy      string                 `json:"createdBy"`
	ApprovedBy     *string                `json:"approvedBy,omitempty"`
	SubmittedBy    *string                `json:"submittedBy,omitempty"`
	SubmittedAt    *time.Time             `json:"submittedAt,omitempty"`
	EffectiveFrom  *time.Time             `json:"effectiveFrom,omitempty"`
	EffectiveTo    *time.Time             `json:"effectiveTo,omitempty"`
	CreatedAt      time.Time              `json:"createdAt"`
	UpdatedAt      time.Time              `json:"updatedAt"`
	ETag           string                 `json:"etag,omitempty"`
}

func getPolicy(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, id string) (*policyRow, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx,
		`SET LOCAL app.tenant_id = $1`, tenantID.String()); err != nil {
		return nil, err
	}

	var p policyRow
	var subjectRaw, resourceRaw, condRaw, obligRaw, filterRaw, masksRaw []byte

	err = conn.QueryRow(ctx, `
		SELECT
			id::text, tenant_id::text, name, version, status, effect,
			subject_match, resource_match, action,
			conditions, obligations, allowed_columns, denied_columns,
			row_filter, column_masks,
			created_by::text, approved_by::text, submitted_by::text,
			submitted_at, effective_from, effective_to,
			created_at, updated_at, etag
		FROM policies
		WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	).Scan(
		&p.ID, &p.TenantID, &p.Name, &p.Version, &p.Status, &p.Effect,
		&subjectRaw, &resourceRaw, &p.Action,
		&condRaw, &obligRaw, &p.AllowedColumns, &p.DeniedColumns,
		&filterRaw, &masksRaw,
		&p.CreatedBy, &p.ApprovedBy, &p.SubmittedBy,
		&p.SubmittedAt, &p.EffectiveFrom, &p.EffectiveTo,
		&p.CreatedAt, &p.UpdatedAt, &p.ETag,
	)
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal(subjectRaw, &p.SubjectMatch)
	_ = json.Unmarshal(resourceRaw, &p.ResourceMatch)
	_ = json.Unmarshal(condRaw, &p.Conditions)
	_ = json.Unmarshal(obligRaw, &p.Obligations)
	_ = json.Unmarshal(filterRaw, &p.RowFilter)
	_ = json.Unmarshal(masksRaw, &p.ColumnMasks)

	return &p, nil
}

func listPolicies(
	ctx context.Context, pool *pgxpool.Pool,
	tenantID uuid.UUID, status, action, cursor string, limit int,
) ([]policyRow, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx,
		`SET LOCAL app.tenant_id = $1`, tenantID.String()); err != nil {
		return nil, err
	}

	q := `
		SELECT
			id::text, tenant_id::text, name, version, status, effect,
			subject_match, resource_match, action,
			conditions, obligations, allowed_columns, denied_columns,
			row_filter, column_masks,
			created_by::text, approved_by::text, submitted_by::text,
			submitted_at, effective_from, effective_to,
			created_at, updated_at, etag
		FROM policies
		WHERE tenant_id = $1`

	args := []interface{}{tenantID}
	idx := 2

	if status != "" {
		q += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, status)
		idx++
	}
	if action != "" {
		q += fmt.Sprintf(" AND action = $%d", idx)
		args = append(args, action)
		idx++
	}
	if cursor != "" {
		q += fmt.Sprintf(" AND updated_at < $%d", idx)
		args = append(args, cursor)
		idx++
	}
	q += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", idx)
	args = append(args, limit+1)

	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []policyRow
	for rows.Next() {
		var p policyRow
		var subjectRaw, resourceRaw, condRaw, obligRaw, filterRaw, masksRaw []byte
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.Name, &p.Version, &p.Status, &p.Effect,
			&subjectRaw, &resourceRaw, &p.Action,
			&condRaw, &obligRaw, &p.AllowedColumns, &p.DeniedColumns,
			&filterRaw, &masksRaw,
			&p.CreatedBy, &p.ApprovedBy, &p.SubmittedBy,
			&p.SubmittedAt, &p.EffectiveFrom, &p.EffectiveTo,
			&p.CreatedAt, &p.UpdatedAt, &p.ETag,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(subjectRaw, &p.SubjectMatch)
		_ = json.Unmarshal(resourceRaw, &p.ResourceMatch)
		_ = json.Unmarshal(condRaw, &p.Conditions)
		_ = json.Unmarshal(obligRaw, &p.Obligations)
		_ = json.Unmarshal(filterRaw, &p.RowFilter)
		_ = json.Unmarshal(masksRaw, &p.ColumnMasks)
		out = append(out, p)
	}
	return out, rows.Err()
}

func writePolicyAudit(
	ctx context.Context, tx pgx.Tx,
	sess *SessionContext,
	policyID, action string,
	afterState interface{},
) error {
	afterJSON, _ := json.Marshal(afterState)
	_, err := tx.Exec(ctx, `
		INSERT INTO policy_audit (tenant_id, policy_id, action, actor_id, after_state, request_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sess.TenantID, policyID, action, sess.UserID,
		string(afterJSON), sess.RequestID,
	)
	return err
}

// withTxSession opens a transaction, sets RLS GUCs, runs fn, then commits.
func withTxSession(ctx context.Context, pool *pgxpool.Pool, sess *SessionContext, fn func(context.Context, pgx.Tx) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, stmt := range []string{
		fmt.Sprintf("SET LOCAL ROLE app_write"),
		fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", sess.TenantID),
		fmt.Sprintf("SET LOCAL app.user_id = '%s'", sess.UserID),
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return err
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── misc helpers ─────────────────────────────────────────────────────────────

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func jsonMarshal(v interface{}) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func jsonMarshalDefault(v interface{}, def string) ([]byte, error) {
	if v == nil {
		return []byte(def), nil
	}
	return json.Marshal(v)
}

func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func nextCursorFromRows(rows []policyRow, limit int) string {
	if len(rows) <= limit {
		return ""
	}
	last := rows[len(rows)-1]
	return last.UpdatedAt.Format(time.RFC3339Nano)
}

func stripQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errBody(code, message string) map[string]string {
	return map[string]string{"code": code, "message": message}
}

func readBody(r *http.Request, maxBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	if int64(len(buf)) >= maxBytes {
		return nil, errors.New("body too large")
	}
	return buf, nil
}

// ── Placeholder for int64 used only in format ─────────────────────────────
var _ = strconv.FormatInt
