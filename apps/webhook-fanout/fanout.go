package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
)

// circuitState tracks per-subscription circuit breaker.
type circuitState struct {
	failures  int
	total     int
	windowEnd time.Time
	open      bool
	openUntil time.Time
}

// Fanout dispatches a Kafka event to all matching subscriptions.
type Fanout struct {
	store     *Store
	vault     *VaultClient
	deliverer *Deliverer
	cfg       config
	log       zerolog.Logger
	circuits  map[string]*circuitState // subscriptionID → state
}

func newFanout(store *Store, vault *VaultClient, deliverer *Deliverer, cfg config, log zerolog.Logger) *Fanout {
	return &Fanout{
		store:     store,
		vault:     vault,
		deliverer: deliverer,
		cfg:       cfg,
		log:       log,
		circuits:  make(map[string]*circuitState),
	}
}

// Dispatch fans out a raw Kafka event to all matching webhook subscriptions.
func (f *Fanout) Dispatch(ctx context.Context, eventType string, raw json.RawMessage) {
	_, span := tracer.Start(ctx, "fanout.dispatch")
	defer span.End()
	span.SetAttributes(attribute.String("event_type", eventType))

	subs, err := f.store.ActiveSubscriptionsForEvent(ctx, eventType)
	if err != nil {
		f.log.Error().Err(err).Str("event_type", eventType).Msg("load subscriptions")
		return
	}

	// Decode minimal envelope for event metadata.
	var env struct {
		EventID  string    `json:"event_id"`
		TenantID string    `json:"tenant_id"`
		Time     time.Time `json:"event_time"`
	}
	_ = json.Unmarshal(raw, &env)

	for _, sub := range subs {
		if f.isCircuitOpen(sub.ID) {
			f.log.Info().Str("subscription_id", sub.ID).Msg("circuit open — skipping delivery")
			continue
		}

		secret, err := f.vault.GetSecret(ctx, sub.SecretRef)
		if err != nil {
			f.log.Error().Err(err).Str("secret_ref", sub.SecretRef).Msg("vault secret fetch failed")
			continue
		}

		wev := WebhookEvent{
			EventID:   env.EventID,
			EventType: eventType,
			TenantID:  sub.TenantID,
			Schema:    "v1",
			Timestamp: env.Time,
			Data:      raw,
		}

		go f.deliverWithRetry(ctx, sub, secret, wev)
	}
}

// deliverWithRetry performs exponential backoff delivery up to MaxRetries, then DLQ.
func (f *Fanout) deliverWithRetry(ctx context.Context, sub dbSubscription, secret []byte, ev WebhookEvent) {
	fullSub := Subscription{
		ID:             sub.ID,
		TenantID:       sub.TenantID,
		URL:            sub.URL,
		Secret:         secret,
		EventTypes:     sub.EventTypes,
		FieldAllowlist: sub.FieldAllowlist,
		FilterExpr:     sub.FilterExpr,
	}

	var lastDeliveryID string
	for attempt := 1; attempt <= f.cfg.MaxRetries+1; attempt++ {
		result := f.deliverer.Deliver(ctx, fullSub, ev)

		status := "delivered"
		var statusCode *int
		if result.StatusCode > 0 {
			sc := result.StatusCode
			statusCode = &sc
		}
		if result.Err != nil {
			status = "failed"
		}

		var nextRetry *time.Time
		if status == "failed" && attempt <= f.cfg.MaxRetries {
			if attempt-1 < len(f.cfg.RetrySchedule) {
				t := time.Now().Add(f.cfg.RetrySchedule[attempt-1])
				nextRetry = &t
			}
		}

		rec := deliveryRecord{
			SubscriptionID: sub.ID,
			EventID:        ev.EventID,
			EventType:      ev.EventType,
			Attempt:        attempt,
			Status:         status,
			StatusCode:     statusCode,
			ResponseBody:   result.ResponseBody,
			DurationMs:     result.DurationMs,
			NextRetryAt:    nextRetry,
		}
		if err := f.store.RecordDelivery(ctx, rec); err != nil {
			f.log.Error().Err(err).Msg("record delivery")
		} else {
			lastDeliveryID = fmt.Sprintf("%s:%d", sub.ID, attempt)
		}

		if status == "delivered" {
			f.recordCircuitSuccess(sub.ID)
			return
		}

		f.recordCircuitFailure(sub.ID)

		if nextRetry == nil {
			break
		}

		select {
		case <-time.After(time.Until(*nextRetry)):
		case <-ctx.Done():
			return
		}
	}

	// All retries exhausted — move to DLQ.
	payload, _ := json.Marshal(ev)
	lastErr := ""
	metricDLQTotal.WithLabelValues(sub.TenantID).Inc()
	metricDeliveryTotal.WithLabelValues("dlq").Inc()
	if err := f.store.MoveToDLQ(ctx, lastDeliveryID, sub.ID, sub.TenantID, payload, lastErr); err != nil {
		f.log.Error().Err(err).Str("subscription_id", sub.ID).Msg("move to DLQ")
	}
}

func (f *Fanout) isCircuitOpen(subID string) bool {
	s := f.circuits[subID]
	if s == nil {
		return false
	}
	if s.open && time.Now().Before(s.openUntil) {
		return true
	}
	s.open = false
	return false
}

func (f *Fanout) recordCircuitFailure(subID string) {
	s := f.getOrCreateCircuit(subID)
	if time.Now().After(s.windowEnd) {
		s.failures = 0
		s.total = 0
		s.windowEnd = time.Now().Add(f.cfg.CircuitBreakerWin)
	}
	s.failures++
	s.total++
	rate := float64(s.failures) / float64(s.total)
	if s.total >= 10 && rate >= f.cfg.CircuitBreakerRate {
		s.open = true
		s.openUntil = time.Now().Add(5 * time.Minute)
	}
}

func (f *Fanout) recordCircuitSuccess(subID string) {
	s := f.getOrCreateCircuit(subID)
	s.total++
}

func (f *Fanout) getOrCreateCircuit(subID string) *circuitState {
	if f.circuits[subID] == nil {
		f.circuits[subID] = &circuitState{windowEnd: time.Now().Add(f.cfg.CircuitBreakerWin)}
	}
	return f.circuits[subID]
}
