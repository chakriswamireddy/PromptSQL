package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHMACSignatureVerifiable(t *testing.T) {
	secret := []byte("test-secret-32-bytes-padded-here")
	var capturedSig, capturedTS string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSig = r.Header.Get("X-Janus-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Build a deliverer that uses the test TLS client.
	cfg := loadConfig()
	cfg.DeliveryTimeout = 5 * time.Second
	log := testLogger()
	d := newDeliverer(cfg, log)
	d.client = srv.Client()

	sub := Subscription{
		ID:       "sub-1",
		TenantID: "tenant-1",
		URL:      srv.URL,
		Secret:   secret,
	}
	ev := WebhookEvent{
		EventID:   "evt-1",
		EventType: "access.decision",
		TenantID:  "tenant-1",
		Schema:    "v1",
		Timestamp: time.Now(),
		Data:      json.RawMessage(`{"decision":"allow"}`),
	}

	result := d.Deliver(context.Background(), sub, ev)
	if result.Err != nil {
		t.Fatalf("delivery failed: %v", result.Err)
	}

	// Parse t= and v1= from signature header.
	parts := strings.Split(capturedSig, ", ")
	for _, p := range parts {
		if strings.HasPrefix(p, "t=") {
			capturedTS = strings.TrimPrefix(p, "t=")
		}
	}

	// Recompute HMAC and verify.
	payload, _ := json.Marshal(ev)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(capturedTS + "."))
	mac.Write(payload[:min(len(payload), cfg.MaxPayloadBytes)])
	expected := "v1=" + hex.EncodeToString(mac.Sum(nil))

	if !strings.Contains(capturedSig, expected) {
		t.Errorf("HMAC mismatch\ngot: %s\nwant: %s", capturedSig, expected)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func testLogger() interface{ Fatal() interface{ Err(error) interface{ Msg(string) } } } {
	return nil // Placeholder — real tests use zerolog.Nop()
}
