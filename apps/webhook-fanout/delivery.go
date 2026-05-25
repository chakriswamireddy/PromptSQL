package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("webhook-fanout")

// Subscription holds the active subscription data resolved from DB.
type Subscription struct {
	ID             string
	TenantID       string
	URL            string
	Secret         []byte // fetched from Vault
	EventTypes     []string
	FieldAllowlist []string
	FilterExpr     string
}

// WebhookEvent is the canonical payload envelope.
type WebhookEvent struct {
	EventID   string          `json:"event_id"`
	EventType string          `json:"event_type"`
	TenantID  string          `json:"tenant_id"`
	Schema    string          `json:"schema_version"` // "v1"
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// DeliveryResult is returned from a single delivery attempt.
type DeliveryResult struct {
	StatusCode   int
	ResponseBody string
	DurationMs   int64
	Err          error
}

type Deliverer struct {
	cfg    config
	log    zerolog.Logger
	client *http.Client
}

func newDeliverer(cfg config, log zerolog.Logger) *Deliverer {
	return &Deliverer{
		cfg: cfg,
		log: log,
		client: &http.Client{
			Timeout: cfg.DeliveryTimeout,
			// Custom dialer to pin the resolved IP (anti-DNS-rebinding).
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Deliver performs a single webhook POST with HMAC signing.
func (d *Deliverer) Deliver(ctx context.Context, sub Subscription, ev WebhookEvent) DeliveryResult {
	ctx, span := tracer.Start(ctx, "webhook.deliver")
	defer span.End()
	span.SetAttributes(
		attribute.String("subscription_id", sub.ID),
		attribute.String("tenant_id", sub.TenantID),
		attribute.String("event_type", ev.EventType),
	)

	// 1. SSRF guard — resolve and pin.
	_, err := validateWebhookURL(ctx, sub.URL)
	if err != nil {
		span.RecordError(err)
		return DeliveryResult{Err: fmt.Errorf("SSRF check: %w", err)}
	}

	// 2. Apply field allowlist.
	payload, err := applyFieldAllowlist(ev, sub.FieldAllowlist)
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("allowlist: %w", err)}
	}

	// 3. Cap payload size.
	if len(payload) > d.cfg.MaxPayloadBytes {
		payload = payload[:d.cfg.MaxPayloadBytes]
	}

	// 4. Build HMAC signature: HMAC-SHA256( t=<unix>.<body> ).
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, sub.Secret)
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	// 5. Build request.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Janus-Signature", "t="+ts+", v1="+sig)
	req.Header.Set("X-Janus-Event-Type", ev.EventType)
	req.Header.Set("X-Janus-Event-Id", ev.EventID)
	req.Header.Set("X-Janus-Idempotency-Key", sub.ID+":"+ev.EventID)
	req.Header.Set("X-Janus-Schema-Version", ev.Schema)

	// 6. Execute.
	start := time.Now()
	resp, err := d.client.Do(req)
	durationMs := time.Since(start).Milliseconds()

	if err != nil {
		metricDeliveryTotal.WithLabelValues("failed").Inc()
		metricDeliveryDuration.WithLabelValues("failed").Observe(time.Since(start).Seconds())
		return DeliveryResult{DurationMs: durationMs, Err: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	result := DeliveryResult{
		StatusCode:   resp.StatusCode,
		ResponseBody: string(body),
		DurationMs:   durationMs,
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		metricDeliveryTotal.WithLabelValues("delivered").Inc()
		metricDeliveryDuration.WithLabelValues("delivered").Observe(time.Since(start).Seconds())
	} else {
		result.Err = fmt.Errorf("non-2xx status: %d", resp.StatusCode)
		metricDeliveryTotal.WithLabelValues("failed").Inc()
		metricDeliveryDuration.WithLabelValues("failed").Observe(time.Since(start).Seconds())
	}
	return result
}

// applyFieldAllowlist strips fields not in the allowlist. Empty list = all fields.
func applyFieldAllowlist(ev WebhookEvent, allowlist []string) ([]byte, error) {
	if len(allowlist) == 0 {
		return json.Marshal(ev)
	}

	// Decode data into a map and filter.
	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &dataMap); err != nil {
		return json.Marshal(ev)
	}
	filtered := make(map[string]json.RawMessage, len(allowlist))
	for _, f := range allowlist {
		if v, ok := dataMap[f]; ok {
			filtered[f] = v
		}
	}
	ev.Data, _ = json.Marshal(filtered)
	return json.Marshal(ev)
}
