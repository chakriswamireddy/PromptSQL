package governance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WebhookClient manages webhook subscriptions.
type WebhookClient struct{ c *Client }

// WebhookSubscription represents a registered webhook endpoint.
type WebhookSubscription struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	URL         string    `json:"url"`
	EventTypes  []string  `json:"event_types"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateSubscription registers a new webhook endpoint.
func (w *WebhookClient) CreateSubscription(ctx context.Context, tenantID, url string, eventTypes []string) (*WebhookSubscription, error) {
	var resp WebhookSubscription
	err := w.c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/admin/%s/webhooks", tenantID),
		map[string]interface{}{"url": url, "event_types": eventTypes},
		&resp)
	return &resp, err
}

// ListSubscriptions returns all webhook subscriptions for a tenant.
func (w *WebhookClient) ListSubscriptions(ctx context.Context, tenantID string) ([]WebhookSubscription, error) {
	var resp struct {
		Subscriptions []WebhookSubscription `json:"subscriptions"`
	}
	err := w.c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/admin/%s/webhooks", tenantID), nil, &resp)
	return resp.Subscriptions, err
}

// DeleteSubscription removes a webhook endpoint.
func (w *WebhookClient) DeleteSubscription(ctx context.Context, tenantID, subscriptionID string) error {
	return w.c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/v1/admin/%s/webhooks/%s", tenantID, subscriptionID), nil, nil)
}

// VerifySignature verifies the HMAC-SHA256 signature on an incoming webhook.
// signature is the value of the X-Governance-Signature header.
// secret is the webhook secret returned at subscription creation.
func VerifySignature(payload []byte, signature, secret string) error {
	// Signature format: "sha256=<hex>"
	parts := strings.SplitN(signature, "=", 2)
	if len(parts) != 2 || parts[0] != "sha256" {
		return fmt.Errorf("invalid signature format")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// ComplianceClient provides compliance-related operations.
type ComplianceClient struct{ c *Client }

// GenerateAccessReview triggers quarterly access review generation.
func (cc *ComplianceClient) GenerateAccessReview(ctx context.Context, tenantID string) (string, error) {
	var resp struct {
		ReviewID string `json:"review_id"`
		Period   string `json:"period"`
	}
	err := cc.c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/admin/%s/access-reviews/generate", tenantID), nil, &resp)
	return resp.ReviewID, err
}

// ExportSIEM exports audit events in CEF or JSON format for SIEM ingestion.
func (cc *ComplianceClient) ExportSIEM(ctx context.Context, tenantID, format string, from, to time.Time) (string, error) {
	path := fmt.Sprintf("/v1/admin/%s/audit/export/siem?format=%s&from=%s&to=%s",
		tenantID, format, from.Format(time.RFC3339), to.Format(time.RFC3339))
	var raw []byte
	// Use raw get for streaming.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cc.c.baseURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+cc.c.token)
	resp, err := cc.c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	import_io "io"
	raw, err = import_io.ReadAll(resp.Body)
	return string(raw), err
}

// AuditClient queries audit logs.
type AuditClient struct{ c *Client }

// QueryRequest specifies filters for audit log queries.
type QueryRequest struct {
	TenantID     string    `json:"tenant_id"`
	ActorID      string    `json:"actor_id,omitempty"`
	Action       string    `json:"action,omitempty"`
	ResourceType string    `json:"resource_type,omitempty"`
	From         time.Time `json:"from,omitempty"`
	To           time.Time `json:"to,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	Offset       int       `json:"offset,omitempty"`
}

// AuditEvent represents a single audit log entry.
type AuditEvent struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	ActorID      string    `json:"actor_id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Outcome      string    `json:"outcome"`
	CreatedAt    time.Time `json:"created_at"`
}

// Query fetches audit events matching the given filters.
func (a *AuditClient) Query(ctx context.Context, req QueryRequest) ([]AuditEvent, error) {
	path := fmt.Sprintf("/v1/admin/%s/audit?", req.TenantID)
	if req.ActorID != "" {
		path += "actor_id=" + req.ActorID + "&"
	}
	if req.Action != "" {
		path += "action=" + req.Action + "&"
	}
	if req.Limit > 0 {
		path += fmt.Sprintf("limit=%d&", req.Limit)
	}

	var resp struct {
		Events []AuditEvent `json:"events"`
	}
	if err := a.c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

// RetrievalClient performs permission-aware retrieval.
type RetrievalClient struct{ c *Client }

// SearchRequest specifies a semantic search query.
type SearchRequest struct {
	TenantID  string `json:"tenant_id"`
	Query     string `json:"query"`
	CorpusID  string `json:"corpus_id"`
	TopK      int    `json:"top_k,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
}

// SearchResult is a single retrieved chunk.
type SearchResult struct {
	ChunkID    string  `json:"chunk_id"`
	DocumentID string  `json:"document_id"`
	Score      float64 `json:"score"`
	Content    string  `json:"content"`
	Allowed    bool    `json:"allowed"`
}

// Search performs a permission-aware semantic search.
func (r *RetrievalClient) Search(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
	var resp struct {
		Results []SearchResult `json:"results"`
	}
	if err := r.c.do(ctx, http.MethodPost, "/v1/retrieval/search", req, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}
