// Package riskwatch subscribes to Redis risk-score updates for connected users
// and signals the proxy to apply mid-flight masking or terminate streaming queries.
package riskwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const tracerName = "proxy.riskwatch"

// Action is the mid-flight response decision.
type Action int

const (
	ActionNone      Action = iota
	ActionMask             // apply heightened masking to remaining rows
	ActionTerminate        // terminate the streaming query immediately
)

// Update carries a risk-score change for a user.
type Update struct {
	TenantID    string `json:"tenant_id"`
	UserID      string `json:"user_id"`
	Score       int    `json:"score"`
	Action      Action `json:"-"`
}

// scorePayload is the Redis hash shape published by the anomaly-detector.
type scorePayload struct {
	TenantID     string `json:"tenant_id"`
	UserID       string `json:"user_id"`
	DecayedTotal int    `json:"decayed_total"`
}

// ActionForScore returns the mid-flight action for the given risk score.
func ActionForScore(score int) Action {
	switch {
	case score >= 96:
		return ActionTerminate
	case score >= 86:
		return ActionMask
	default:
		return ActionNone
	}
}

func actionFromScore(score int) Action { return ActionForScore(score) }

// Listener is the set of listeners registered for a user key.
type Listener struct {
	ch chan Update
}

// Watcher subscribes to risk score updates from Redis pub/sub and notifies
// any registered per-connection listeners for active streaming queries.
type Watcher struct {
	rdb *redis.Client
	mu  sync.RWMutex
	// listeners: key = "tenantID:userID" → slice of active listeners.
	listeners map[string][]*Listener
}

// New creates a Watcher.
func New(rdb *redis.Client) *Watcher {
	return &Watcher{
		rdb:       rdb,
		listeners: make(map[string][]*Listener),
	}
}

// Subscribe registers a channel to receive risk score updates for the given
// tenant/user while a streaming query is active.
// The caller must call Unsubscribe when the query finishes.
func (w *Watcher) Subscribe(tenantID, userID string) (<-chan Update, func()) {
	key := userKey(tenantID, userID)
	lst := &Listener{ch: make(chan Update, 4)}

	w.mu.Lock()
	w.listeners[key] = append(w.listeners[key], lst)
	w.mu.Unlock()

	unsub := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		list := w.listeners[key]
		for i, l := range list {
			if l == lst {
				w.listeners[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(w.listeners[key]) == 0 {
			delete(w.listeners, key)
		}
	}
	return lst.ch, unsub
}

// Run starts the Redis pub/sub subscription loop. Blocks until ctx is done.
// The channel pattern "risk:score:*" matches all score updates published by
// the anomaly-detector's Redis sink.
func (w *Watcher) Run(ctx context.Context) error {
	pubsub := w.rdb.PSubscribe(ctx, "risk:score:*")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("riskwatch: pubsub channel closed")
			}
			w.dispatch(ctx, msg.Payload)
		}
	}
}

func (w *Watcher) dispatch(ctx context.Context, payload string) {
	var p scorePayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return
	}

	action := actionFromScore(p.DecayedTotal)
	if action == ActionNone {
		return
	}

	_, span := otel.Tracer(tracerName).Start(ctx, "riskwatch.dispatch")
	defer span.End()
	span.SetAttributes(
		attribute.String("proxy.tenant_id", p.TenantID),
		attribute.String("proxy.user_id", p.UserID),
		attribute.Int("proxy.risk_score", p.DecayedTotal),
		attribute.Int("proxy.mid_flight_action", int(action)),
	)

	key := userKey(p.TenantID, p.UserID)
	w.mu.RLock()
	list := w.listeners[key]
	w.mu.RUnlock()

	update := Update{
		TenantID: p.TenantID,
		UserID:   p.UserID,
		Score:    p.DecayedTotal,
		Action:   action,
	}
	for _, l := range list {
		select {
		case l.ch <- update:
		default:
			// Non-blocking: if the listener's buffer is full, skip.
			// The proxy stream handler will catch up on the next row.
		}
	}
}

func userKey(tenantID, userID string) string {
	return tenantID + ":" + userID
}
