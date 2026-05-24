// Package outbox implements the transactional outbox relay.
// It polls outbox_events for unsent rows and publishes them to Redis pub/sub,
// giving the PDP cache invalidation its delivery guarantee.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/governance-platform/pkg/logging"
)

const (
	pollInterval  = 2 * time.Second
	batchSize     = 50
	channelPrefix = "policy.invalidate."
)

// Relay polls outbox_events and publishes pending rows to Redis pub/sub.
type Relay struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	log  *logging.Logger
}

func New(pool *pgxpool.Pool, rdb *redis.Client, log *logging.Logger) *Relay {
	return &Relay{pool: pool, rdb: rdb, log: log}
}

// Run starts the relay loop; returns when ctx is cancelled.
func (r *Relay) Run(ctx context.Context) {
	r.log.Info().Msg("outbox relay started")
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info().Msg("outbox relay stopping")
			return
		case <-ticker.C:
			if err := r.flush(ctx); err != nil {
				r.log.Error().Err(err).Msg("outbox relay flush error")
			}
		}
	}
}

func (r *Relay) flush(ctx context.Context) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	// Lock + load a batch of unsent events.
	rows, err := conn.Query(ctx, `
		SELECT id::text, tenant_id::text, kind, payload
		FROM outbox_events
		WHERE sent_at IS NULL
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`,
		batchSize,
	)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	type event struct {
		id       string
		tenantID string
		kind     string
		payload  json.RawMessage
	}

	var events []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.tenantID, &e.kind, &e.payload); err != nil {
			r.log.Warn().Err(err).Msg("outbox scan error")
			continue
		}
		events = append(events, e)
	}
	rows.Close()

	if len(events) == 0 {
		return nil
	}

	for _, e := range events {
		channel := channelPrefix + e.tenantID
		msg := map[string]interface{}{
			"kind":     e.kind,
			"payload":  e.payload,
			"eventId":  e.id,
			"emittedAt": time.Now().UTC().Format(time.RFC3339),
		}
		msgJSON, _ := json.Marshal(msg)

		if err := r.rdb.Publish(ctx, channel, msgJSON).Err(); err != nil {
			r.log.Error().Err(err).Str("event_id", e.id).Msg("outbox publish error")
			continue
		}

		// Mark as sent in the same connection (not transaction — if we crash
		// here the PDP poller will re-read from DB, so double-publish is safe).
		if _, err := conn.Exec(ctx,
			`UPDATE outbox_events SET sent_at = now() WHERE id = $1`, e.id,
		); err != nil {
			r.log.Warn().Err(err).Str("event_id", e.id).Msg("outbox mark-sent error")
		}
	}

	r.log.Info().Int("count", len(events)).Msg("outbox flushed")
	return nil
}
