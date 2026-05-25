package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// Store handles all DB interactions for webhook-fanout.
type Store struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

func newStore(ctx context.Context, dsn string, log zerolog.Logger) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return &Store{pool: pool, log: log}, nil
}

func (s *Store) Close() { s.pool.Close() }

// ActiveSubscriptionsForEvent returns subscriptions that match the given event type.
// Runs as the service role — subscriptions are fetched across all tenants.
func (s *Store) ActiveSubscriptionsForEvent(ctx context.Context, eventType string) ([]dbSubscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, url, event_types, secret_ref, field_allowlist, filter_expr, failure_count
		FROM webhook_subscriptions
		WHERE is_active = true AND $1 = ANY(event_types)
	`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []dbSubscription
	for rows.Next() {
		var sub dbSubscription
		if err := rows.Scan(&sub.ID, &sub.TenantID, &sub.URL, &sub.EventTypes,
			&sub.SecretRef, &sub.FieldAllowlist, &sub.FilterExpr, &sub.FailureCount); err != nil {
			s.log.Error().Err(err).Msg("scan subscription row")
			continue
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

type dbSubscription struct {
	ID             string
	TenantID       string
	URL            string
	EventTypes     []string
	SecretRef      string
	FieldAllowlist []string
	FilterExpr     string
	FailureCount   int
}

// RecordDelivery writes a delivery attempt row.
func (s *Store) RecordDelivery(ctx context.Context, d deliveryRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_deliveries
		  (subscription_id, event_id, event_type, attempt, status, status_code, response_body, duration_ms, attempted_at, next_retry_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW(),$9)
	`, d.SubscriptionID, d.EventID, d.EventType, d.Attempt, d.Status,
		d.StatusCode, d.ResponseBody, d.DurationMs, d.NextRetryAt)
	return err
}

type deliveryRecord struct {
	SubscriptionID string
	EventID        string
	EventType      string
	Attempt        int
	Status         string
	StatusCode     *int
	ResponseBody   string
	DurationMs     int64
	NextRetryAt    *time.Time
}

// MoveToDLQ inserts the event into webhook_dlq and marks the delivery as dlq.
func (s *Store) MoveToDLQ(ctx context.Context, deliveryID, subscriptionID, tenantID string, payload json.RawMessage, lastError string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		INSERT INTO webhook_dlq (delivery_id, subscription_id, tenant_id, event_payload, last_error)
		VALUES ($1,$2,$3,$4,$5)
	`, deliveryID, subscriptionID, tenantID, payload, lastError)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET status='dlq' WHERE id=$1`, deliveryID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		UPDATE webhook_subscriptions
		SET failure_count = failure_count + 1
		WHERE id = $1
	`, subscriptionID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IncrementFailureCount bumps the failure counter and auto-deactivates after threshold.
func (s *Store) IncrementFailureCount(ctx context.Context, subscriptionID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_subscriptions
		SET failure_count = failure_count + 1,
		    is_active = CASE WHEN failure_count + 1 >= 100 THEN false ELSE is_active END
		WHERE id = $1
	`, subscriptionID)
	return err
}

// ScheduledQueries returns saved questions due for execution.
func (s *Store) ScheduledQueries(ctx context.Context) ([]savedQuestion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, schedule_cron, last_run_at
		FROM saved_questions
		WHERE schedule_enabled = true
		  AND (next_run_at IS NULL OR next_run_at <= NOW())
		ORDER BY next_run_at NULLS FIRST
		LIMIT 1000
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var qs []savedQuestion
	for rows.Next() {
		var q savedQuestion
		if err := rows.Scan(&q.ID, &q.TenantID, &q.ScheduleCron, &q.LastRunAt); err != nil {
			continue
		}
		qs = append(qs, q)
	}
	return qs, rows.Err()
}

type savedQuestion struct {
	ID           string
	TenantID     string
	ScheduleCron string
	LastRunAt    *time.Time
}

// MarkScheduledQueryRan updates last_run_at + next_run_at after execution.
func (s *Store) MarkScheduledQueryRan(ctx context.Context, id string, nextRun time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE saved_questions
		SET last_run_at = NOW(), next_run_at = $1
		WHERE id = $2
	`, nextRun, id)
	return err
}
