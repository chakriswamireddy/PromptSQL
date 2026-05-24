package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pdpv1 "github.com/governance-platform/pkg/pdpv1"
)

// SimulatorHandler handles simulate and diff endpoints.
type SimulatorHandler struct {
	pool    *pgxpool.Pool
	pdpConn pdpv1.PDPClient
}

func NewSimulatorHandler(pool *pgxpool.Pool, pdpConn pdpv1.PDPClient) *SimulatorHandler {
	return &SimulatorHandler{pool: pool, pdpConn: pdpConn}
}

// Simulate handles POST /v1/admin/{tenantSlug}/policies/simulate (spot check).
func (h *SimulatorHandler) Simulate(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.simulator.spot")
	defer span.End()

	sess := SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	body, err := readBody(r, 64*1024)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_body", err.Error()))
		return
	}

	var req struct {
		PolicyIDs    []string               `json:"policyIds"`
		UseDraft     string                 `json:"useDraft"`
		Subject      map[string]interface{} `json:"subject"`
		Action       string                 `json:"action"`
		Resource     string                 `json:"resource"`
		DataSourceID string                 `json:"dataSourceId"`
		Context      map[string]string      `json:"context"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_json", err.Error()))
		return
	}

	// Delegate to PDP Explain RPC. Build a minimal SessionContext from the
	// subject field; for a real user ID, load full roles/attrs from DB.
	subjectSession := buildSubjectSession(ctx, h.pool, req.Subject, sess)

	ctxMap := req.Context
	if ctxMap == nil {
		ctxMap = map[string]string{}
	}
	if req.UseDraft != "" {
		ctxMap["draft_policy_id"] = req.UseDraft
	}
	result, err := h.pdpConn.Explain(ctx, &pdpv1.DecideRequest{
		SubjectSessionContext: mustMarshal(subjectSession),
		Action:                req.Action,
		Resource:              req.Resource,
		DataSourceId:          req.DataSourceID,
		Context:               ctxMap,
	})
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("pdp_error", err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// SimulateDiff handles POST /v1/admin/{tenantSlug}/policies/simulate/diff
func (h *SimulatorHandler) SimulateDiff(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.simulator.diff")
	defer span.End()

	sess := SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	body, err := readBody(r, 16*1024)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_body", err.Error()))
		return
	}

	var req struct {
		DraftPolicyID string `json:"draftPolicyId"`
		SampleSize    int    `json:"sampleSize"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_json", err.Error()))
		return
	}
	if req.SampleSize <= 0 {
		req.SampleSize = 20
	}
	if req.SampleSize > 200 {
		req.SampleSize = 200
	}

	// Compute hashes for cache key.
	draftHash, activeHash, err := computeDiffHashes(ctx, h.pool, sess.TenantID.String(), req.DraftPolicyID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	// Check cache.
	cached, err := loadCachedDiff(ctx, h.pool, sess.TenantID.String(), draftHash, activeHash, req.SampleSize)
	if err == nil && cached != nil {
		writeJSON(w, http.StatusOK, cached)
		return
	}

	// Run diff against PDP.
	report, err := h.runDiff(ctx, sess, req.DraftPolicyID, req.SampleSize, draftHash, activeHash)
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("diff_error", err.Error()))
		return
	}

	// Persist to cache table.
	_ = saveDiffReport(ctx, h.pool, sess.TenantID.String(), sess.UserID.String(), report)

	writeJSON(w, http.StatusOK, report)
}

func (h *SimulatorHandler) runDiff(
	ctx context.Context,
	sess *auth.SessionContext,
	draftPolicyID string,
	sampleSize int,
	draftHash, activeHash string,
) (map[string]interface{}, error) {
	// Sample users by role from the tenant.
	users, err := sampleUsersByRole(ctx, h.pool, sess.TenantID.String(), sampleSize)
	if err != nil {
		return nil, err
	}

	perRoleDiff := make(map[string]roleDiffAccum)
	var totalAffected int

	newlyPermittedCols := map[string]bool{}
	newlyDeniedCols := map[string]bool{}
	newObligations := map[string]bool{}
	var newlyBlockedRows, newlyPermittedRows int

	for _, u := range users {
		// Decide with active policy set.
		activeResult, _ := h.pdpConn.Decide(ctx, &pdpv1.DecideRequest{
			SubjectSessionContext: mustMarshal(u.session),
			Action:                "read",
			Resource:              u.resource,
			DataSourceId:          u.dataSourceID,
		})

		// Decide with draft replacing its name (pass via context map).
		draftResult, _ := h.pdpConn.Decide(ctx, &pdpv1.DecideRequest{
			SubjectSessionContext: mustMarshal(u.session),
			Action:                "read",
			Resource:              u.resource,
			DataSourceId:          u.dataSourceID,
			Context:               map[string]string{"draft_policy_id": draftPolicyID},
		})

		accum := perRoleDiff[u.role]
		if activeResult != nil && draftResult != nil {
			if activeResult.Effect == "PERMIT" && draftResult.Effect == "DENY" {
				accum.permitToDeny++
				totalAffected++
			}
			if activeResult.Effect == "DENY" && draftResult.Effect == "PERMIT" {
				accum.denyToPermit++
				totalAffected++
			}
		}
		if draftResult != nil {
			for _, c := range draftResult.AllowedColumns {
				newlyPermittedCols[c] = true
			}
			for _, c := range draftResult.DeniedColumns {
				newlyDeniedCols[c] = true
			}
		}
		accum.sampleCount++
		perRoleDiff[u.role] = accum
	}

	severity := computeSeverity(totalAffected, len(newlyPermittedCols), len(newlyDeniedCols))

	perRoleArr := make([]map[string]interface{}, 0, len(perRoleDiff))
	for role, a := range perRoleDiff {
		perRoleArr = append(perRoleArr, map[string]interface{}{
			"role":         role,
			"sampleCount":  a.sampleCount,
			"permitTodeny": a.permitToDeny,
			"denyToPermit": a.denyToPermit,
		})
	}

	keys := func(m map[string]bool) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	newObligsArr := keys(newObligations)
	_ = newObligsArr

	report := map[string]interface{}{
		"id":         newDiffReportID(),
		"draftHash":  draftHash,
		"activeHash": activeHash,
		"sampleSize": sampleSize,
		"createdAt":  time.Now().UTC().Format(time.RFC3339),
		"summary": map[string]interface{}{
			"newlyPermittedColumns":  keys(newlyPermittedCols),
			"newlyDeniedColumns":     keys(newlyDeniedCols),
			"newlyBlockedRows":       newlyBlockedRows,
			"newlyPermittedRows":     newlyPermittedRows,
			"newObligations":         newObligsArr,
			"affectedUsersEstimate":  totalAffected,
			"severity":               severity,
		},
		"perRoleDiff":      perRoleArr,
		"topAffectedUsers": []interface{}{},
	}
	return report, nil
}

type roleDiffAccum struct {
	sampleCount int
	permitToDeny int
	denyToPermit int
}

type sampledUser struct {
	role         string
	resource     string
	dataSourceID string
	session      map[string]interface{}
}

func sampleUsersByRole(ctx context.Context, pool *pgxpool.Pool, tenantID string, n int) ([]sampledUser, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", tenantID)); err != nil {
		return nil, err
	}

	rows, err := conn.Query(ctx, `
		SELECT u.id::text, u.email, r.name AS role,
		       ds.id::text AS ds_id
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		CROSS JOIN LATERAL (
			SELECT id FROM data_sources WHERE tenant_id = u.tenant_id LIMIT 1
		) ds
		WHERE u.tenant_id = $1 AND u.status = 'active'
		ORDER BY random()
		LIMIT $2`, tenantID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []sampledUser
	for rows.Next() {
		var userID, email, role, dsID string
		if err := rows.Scan(&userID, &email, &role, &dsID); err != nil {
			continue
		}
		out = append(out, sampledUser{
			role:         role,
			resource:     "orders",
			dataSourceID: dsID,
			session: map[string]interface{}{
				"userId":   userID,
				"tenantId": tenantID,
				"roles":    []string{role},
			},
		})
	}
	return out, rows.Err()
}

func computeDiffHashes(ctx context.Context, pool *pgxpool.Pool, tenantID, draftID string) (string, string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return "", "", err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", tenantID)); err != nil {
		return "", "", err
	}

	var draftEtag string
	_ = conn.QueryRow(ctx, `SELECT COALESCE(etag,'') FROM policies WHERE id = $1 AND tenant_id = $2`,
		draftID, tenantID).Scan(&draftEtag)

	var activeEtags []string
	rows, _ := conn.Query(ctx, `SELECT COALESCE(etag,'') FROM policies WHERE tenant_id = $1 AND status = 'active' ORDER BY id`,
		tenantID)
	defer rows.Close()
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		activeEtags = append(activeEtags, e)
	}

	h := sha256.New()
	for _, e := range activeEtags {
		h.Write([]byte(e))
	}
	activeHash := hex.EncodeToString(h.Sum(nil))[:16]
	return draftEtag[:min(16, len(draftEtag))], activeHash, nil
}

func loadCachedDiff(ctx context.Context, pool *pgxpool.Pool, tenantID, draftHash, activeHash string, sampleSize int) (map[string]interface{}, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", tenantID)); err != nil {
		return nil, err
	}

	var bodyRaw []byte
	err = conn.QueryRow(ctx, `
		SELECT body FROM policy_diff_reports
		WHERE tenant_id = $1 AND draft_hash = $2 AND active_hash = $3
		AND sample_size = $4 AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`,
		tenantID, draftHash, activeHash, sampleSize,
	).Scan(&bodyRaw)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(bodyRaw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func saveDiffReport(ctx context.Context, pool *pgxpool.Pool, tenantID, createdBy string, report map[string]interface{}) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", tenantID)); err != nil {
		return err
	}

	draftHash, _ := report["draftHash"].(string)
	activeHash, _ := report["activeHash"].(string)
	sampleSize, _ := report["sampleSize"].(int)
	bodyJSON, _ := json.Marshal(report)

	_, err = conn.Exec(ctx, `
		INSERT INTO policy_diff_reports (tenant_id, draft_hash, active_hash, sample_size, body, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, draft_hash, active_hash, sample_size) DO NOTHING`,
		tenantID, draftHash, activeHash, sampleSize, bodyJSON, createdBy,
	)
	return err
}

func computeSeverity(affected, newPermitted, newDenied int) string {
	if affected == 0 && newPermitted == 0 && newDenied == 0 {
		return "none"
	}
	if newDenied > 5 || affected > 50 {
		return "critical"
	}
	if newDenied > 2 || affected > 20 {
		return "high"
	}
	if newPermitted > 5 || affected > 5 {
		return "medium"
	}
	return "low"
}

func buildSubjectSession(ctx context.Context, pool *pgxpool.Pool, subject map[string]interface{}, sess *auth.SessionContext) map[string]interface{} {
	base := map[string]interface{}{
		"tenantId":  sess.TenantID.String(),
		"sessionId": sess.SessionID.String(),
		"roles":     []string{},
		"attributes": map[string]interface{}{},
	}
	if uid, ok := subject["userId"].(string); ok && uid != "" {
		base["userId"] = uid
		// In a real impl, resolve roles/attrs from DB here.
	}
	if attrs, ok := subject["attributes"].(map[string]interface{}); ok {
		base["attributes"] = attrs
	}
	return base
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

func newDiffReportID() string {
	return fmt.Sprintf("diff_%d", time.Now().UnixNano())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
