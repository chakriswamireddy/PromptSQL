package accessreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/accessreview")

type Handler struct {
	db  *store.DB
	log *zap.Logger
}

func NewHandler(db *store.DB, log *zap.Logger) *Handler {
	return &Handler{db: db, log: log}
}

// Generate creates (or refreshes) an access review for the current quarter.
func (h *Handler) Generate(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "accessreview.Generate")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	period := currentQuarter()
	reviewID, err := h.generateReview(ctx, tenantID, period)
	if err != nil {
		h.log.Error("generate access review", zap.Error(err), zap.String("tenant_id", tenantID))
		http.Error(w, `{"code":"internal","message":"failed to generate review"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"review_id": reviewID, "period": period}) //nolint
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "accessreview.List")
	defer span.End()

	tenantID := r.PathValue("tenant_id")

	rows, err := h.db.Pool.Query(ctx,
		`SELECT id, review_period, generated_at, due_at, total_entries, certified_count, revoked_count, status
		   FROM access_reviews WHERE tenant_id = $1 ORDER BY generated_at DESC LIMIT 20`,
		tenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Row struct {
		ID             string    `json:"id"`
		ReviewPeriod   string    `json:"review_period"`
		GeneratedAt    time.Time `json:"generated_at"`
		DueAt          time.Time `json:"due_at"`
		TotalEntries   int       `json:"total_entries"`
		CertifiedCount int       `json:"certified_count"`
		RevokedCount   int       `json:"revoked_count"`
		Status         string    `json:"status"`
	}
	var reviews []Row
	for rows.Next() {
		var rv Row
		if err := rows.Scan(&rv.ID, &rv.ReviewPeriod, &rv.GeneratedAt, &rv.DueAt,
			&rv.TotalEntries, &rv.CertifiedCount, &rv.RevokedCount, &rv.Status); err != nil {
			continue
		}
		reviews = append(reviews, rv)
	}
	if reviews == nil {
		reviews = []Row{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"reviews": reviews}) //nolint
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "accessreview.Get")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	reviewID := r.PathValue("review_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID), attribute.String("review_id", reviewID))

	rows, err := h.db.Pool.Query(ctx,
		`SELECT id, user_id, user_email, role, last_active_at, certified_at, decision, notes
		   FROM access_review_entries WHERE review_id = $1 AND tenant_id = $2 ORDER BY user_email`,
		reviewID, tenantID)
	if err != nil {
		http.Error(w, `{"code":"internal","message":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type Entry struct {
		ID           string     `json:"id"`
		UserID       string     `json:"user_id"`
		UserEmail    string     `json:"user_email"`
		Role         string     `json:"role"`
		LastActiveAt *time.Time `json:"last_active_at,omitempty"`
		CertifiedAt  *time.Time `json:"certified_at,omitempty"`
		Decision     *string    `json:"decision,omitempty"`
		Notes        *string    `json:"notes,omitempty"`
	}
	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.UserID, &e.UserEmail, &e.Role,
			&e.LastActiveAt, &e.CertifiedAt, &e.Decision, &e.Notes); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []Entry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"entries": entries}) //nolint
}

// Certify records a manager's certify/revoke decision on an entry.
func (h *Handler) Certify(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "accessreview.Certify")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	reviewID := r.PathValue("review_id")
	entryID := r.PathValue("entry_id")

	var body struct {
		Decision    string `json:"decision"`
		Notes       string `json:"notes"`
		CertifiedBy string `json:"certified_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"code":"bad_request","message":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if body.Decision != "certify" && body.Decision != "revoke" {
		http.Error(w, `{"code":"bad_request","message":"decision must be certify or revoke"}`, http.StatusBadRequest)
		return
	}

	_, err := h.db.Pool.Exec(ctx,
		`UPDATE access_review_entries
		    SET decision = $1, notes = $2, certified_by = $3, certified_at = now()
		  WHERE id = $4 AND review_id = $5 AND tenant_id = $6`,
		body.Decision, body.Notes, body.CertifiedBy, entryID, reviewID, tenantID)
	if err != nil {
		h.log.Error("certify entry", zap.Error(err))
		http.Error(w, `{"code":"internal","message":"update failed"}`, http.StatusInternalServerError)
		return
	}

	// Update summary counts.
	h.db.Pool.Exec(ctx, //nolint
		`UPDATE access_reviews SET
		    certified_count = (SELECT COUNT(*) FROM access_review_entries WHERE review_id=$1 AND decision='certify'),
		    revoked_count   = (SELECT COUNT(*) FROM access_review_entries WHERE review_id=$1 AND decision='revoke')
		  WHERE id=$1`, reviewID)

	w.WriteHeader(http.StatusNoContent)
}

// generateReview pulls active user×role pairs from policy_audit and inserts review entries.
func (h *Handler) generateReview(ctx context.Context, tenantID, period string) (string, error) {
	tx, err := h.db.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint

	dueAt := time.Now().AddDate(0, 0, 14)
	var reviewID string
	err = tx.QueryRow(ctx,
		`INSERT INTO access_reviews (tenant_id, review_period, due_at, generated_by)
		 VALUES ($1, $2, $3, 'system')
		 ON CONFLICT (tenant_id, review_period) DO UPDATE
		     SET generated_at = now(), status = 'pending'
		 RETURNING id`,
		tenantID, period, dueAt).Scan(&reviewID)
	if err != nil {
		return "", fmt.Errorf("insert review: %w", err)
	}

	// Pull last 90 days of distinct user×role pairs from policy_audit.
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT pa.actor_id, u.email, pa.metadata->>'role'
		   FROM policy_audit pa
		   JOIN users u ON u.id = pa.actor_id AND u.tenant_id = $1
		  WHERE pa.tenant_id = $1
		    AND pa.created_at > now() - interval '90 days'
		    AND pa.metadata->>'role' IS NOT NULL`,
		tenantID)
	if err != nil {
		return "", fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var userID, email, role string
		if err := rows.Scan(&userID, &email, &role); err != nil {
			continue
		}
		tx.Exec(ctx, //nolint
			`INSERT INTO access_review_entries (review_id, tenant_id, user_id, user_email, role, decision)
			 VALUES ($1, $2, $3, $4, $5, 'pending')
			 ON CONFLICT DO NOTHING`,
			reviewID, tenantID, userID, email, role)
		count++
	}

	tx.Exec(ctx, //nolint
		`UPDATE access_reviews SET total_entries = $1 WHERE id = $2`, count, reviewID)

	return reviewID, tx.Commit(ctx)
}

// RunQuarterlyGenerator triggers automatic access review generation at quarter start.
func RunQuarterlyGenerator(ctx context.Context, db *store.DB, log *zap.Logger) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			if t.Day() == 1 && (t.Month() == 1 || t.Month() == 4 || t.Month() == 7 || t.Month() == 10) {
				h := &Handler{db: db, log: log}
				tenants, _ := listTenantIDs(ctx, db)
				for _, tid := range tenants {
					if _, err := h.generateReview(ctx, tid, currentQuarter()); err != nil {
						log.Error("quarterly access review gen", zap.Error(err), zap.String("tenant_id", tid))
					}
				}
			}
		}
	}
}

func currentQuarter() string {
	t := time.Now()
	q := (int(t.Month())-1)/3 + 1
	return fmt.Sprintf("%d-Q%d", t.Year(), q)
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
