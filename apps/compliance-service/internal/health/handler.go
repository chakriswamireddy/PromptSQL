// Package health computes and serves customer health scores.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/health")

type Handler struct {
	db  *store.DB
	log *zap.Logger
}

func NewHandler(db *store.DB, log *zap.Logger) *Handler {
	return &Handler{db: db, log: log}
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "health.Get")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	type Row struct {
		TenantID      string    `json:"tenant_id"`
		ScoreDate     string    `json:"score_date"`
		HealthScore   float64   `json:"health_score"`
		QueriesPerDay int       `json:"queries_per_day"`
		ActiveUsers7d int       `json:"active_users_7d"`
		PolicyCount   int       `json:"policy_count"`
		AIQueries7d   int       `json:"ai_queries_7d"`
		Anomalies7d   int       `json:"anomalies_7d"`
		RiskEvents7d  int       `json:"risk_events_7d"`
		ComputedAt    time.Time `json:"computed_at"`
	}
	var row Row
	err := h.db.Pool.QueryRow(ctx,
		`SELECT tenant_id, score_date::text, health_score, queries_per_day,
		        active_users_7d, policy_count, ai_queries_7d, anomalies_7d, risk_events_7d, computed_at
		   FROM customer_health_scores
		  WHERE tenant_id = $1
		  ORDER BY score_date DESC LIMIT 1`, tenantID).
		Scan(&row.TenantID, &row.ScoreDate, &row.HealthScore, &row.QueriesPerDay,
			&row.ActiveUsers7d, &row.PolicyCount, &row.AIQueries7d,
			&row.Anomalies7d, &row.RiskEvents7d, &row.ComputedAt)
	if err != nil {
		row.TenantID = tenantID
		row.ScoreDate = time.Now().Format("2006-01-02")
		row.HealthScore = 0
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(row) //nolint
}

func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "health.History")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	rows, err := h.db.Pool.Query(ctx,
		`SELECT score_date::text, health_score, queries_per_day, active_users_7d, computed_at
		   FROM customer_health_scores WHERE tenant_id = $1
		  ORDER BY score_date DESC LIMIT 90`, tenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Row struct {
		ScoreDate     string    `json:"score_date"`
		HealthScore   float64   `json:"health_score"`
		QueriesPerDay int       `json:"queries_per_day"`
		ActiveUsers7d int       `json:"active_users_7d"`
		ComputedAt    time.Time `json:"computed_at"`
	}
	var history []Row
	for rows.Next() {
		var r Row
		rows.Scan(&r.ScoreDate, &r.HealthScore, &r.QueriesPerDay, &r.ActiveUsers7d, &r.ComputedAt) //nolint
		history = append(history, r)
	}
	if history == nil {
		history = []Row{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"history": history}) //nolint
}

// RunDailyRollup computes and stores a health score for every tenant daily at 02:00 UTC.
func RunDailyRollup(ctx context.Context, db *store.DB, log *zap.Logger) {
	for {
		next := nextRunAt(2, 0)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}

		tenants, _ := listTenantIDs(ctx, db)
		for _, tid := range tenants {
			if err := computeHealth(ctx, db, tid); err != nil {
				log.Error("health rollup", zap.Error(err), zap.String("tenant_id", tid))
			}
		}
		log.Info("health scores computed", zap.Int("tenants", len(tenants)))
	}
}

func computeHealth(ctx context.Context, db *store.DB, tenantID string) error {
	// Pull signals from various tables.
	var queries, activeUsers, policyCount, aiQueries, anomalies, riskEvents int
	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(*),0) FROM policy_audit
		  WHERE tenant_id=$1 AND created_at > now() - interval '1 day'`, tenantID).
		Scan(&queries) //nolint

	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(DISTINCT actor_id),0) FROM policy_audit
		  WHERE tenant_id=$1 AND created_at > now() - interval '7 days'`, tenantID).
		Scan(&activeUsers) //nolint

	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(*),0) FROM policy_set_versions
		  WHERE tenant_id=$1 AND status='active'`, tenantID).
		Scan(&policyCount) //nolint

	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(*),0) FROM policy_audit
		  WHERE tenant_id=$1 AND action LIKE 'ai_%' AND created_at > now() - interval '7 days'`, tenantID).
		Scan(&aiQueries) //nolint

	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(*),0) FROM risk_events
		  WHERE tenant_id=$1 AND created_at > now() - interval '7 days'`, tenantID).
		Scan(&anomalies) //nolint

	db.Pool.QueryRow(ctx,
		`SELECT COALESCE(COUNT(*),0) FROM risk_events
		  WHERE tenant_id=$1 AND severity IN ('high','critical') AND created_at > now() - interval '7 days'`, tenantID).
		Scan(&riskEvents) //nolint

	// Simple scoring formula (0–100).
	score := 50.0
	if queries > 0 {
		score += 10
	}
	if activeUsers > 0 {
		score += 10
	}
	if policyCount > 2 {
		score += 10
	}
	if aiQueries > 0 {
		score += 5
	}
	// Deduct for anomalies.
	if anomalies > 10 {
		score -= 15
	} else if anomalies > 3 {
		score -= 5
	}
	if riskEvents > 5 {
		score -= 10
	}
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	_, err := db.Pool.Exec(ctx,
		`INSERT INTO customer_health_scores
		    (tenant_id, score_date, health_score, queries_per_day, active_users_7d,
		     policy_count, ai_queries_7d, anomalies_7d, risk_events_7d)
		 VALUES ($1, CURRENT_DATE, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (tenant_id, score_date) DO UPDATE SET
		     health_score    = EXCLUDED.health_score,
		     queries_per_day = EXCLUDED.queries_per_day,
		     active_users_7d = EXCLUDED.active_users_7d,
		     policy_count    = EXCLUDED.policy_count,
		     ai_queries_7d   = EXCLUDED.ai_queries_7d,
		     anomalies_7d    = EXCLUDED.anomalies_7d,
		     risk_events_7d  = EXCLUDED.risk_events_7d,
		     computed_at     = now()`,
		tenantID, score, queries, activeUsers, policyCount, aiQueries, anomalies, riskEvents)
	return err
}

func nextRunAt(hour, minute int) time.Time {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)
	if next.Before(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func listTenantIDs(ctx context.Context, db *store.DB) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `SELECT id FROM tenants WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id) //nolint
		ids = append(ids, id)
	}
	return ids, nil
}
