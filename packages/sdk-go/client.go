// Package governance provides a Go client for the AI-Native Authorization &
// Retrieval Governance Platform API.
package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const sdkVersion = "1.0.0"

// Client is the root client for the Governance Platform API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string

	Auth       *AuthClient
	PDP        *PDPClient
	Audit      *AuditClient
	Retrieval  *RetrievalClient
	Webhooks   *WebhookClient
	Compliance *ComplianceClient
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(cl *Client) { cl.httpClient = c } }

// WithToken sets a static Bearer token (useful for service-to-service calls).
func WithToken(token string) Option { return func(cl *Client) { cl.token = token } }

// New creates a configured Client.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	c.Auth = &AuthClient{c: c}
	c.PDP = &PDPClient{c: c}
	c.Audit = &AuditClient{c: c}
	c.Retrieval = &RetrievalClient{c: c}
	c.Webhooks = &WebhookClient{c: c}
	c.Compliance = &ComplianceClient{c: c}
	return c
}

// SetToken updates the Bearer token (after login).
func (c *Client) SetToken(token string) { c.token = token }

func (c *Client) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "governance-sdk-go/"+sdkVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if json.Unmarshal(respBytes, &apiErr) == nil && apiErr.Code != "" {
			return &apiErr
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(respBytes))
	}

	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}
	}
	return nil
}

// APIError represents a structured API error response.
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("[%s] %s (request_id: %s)", e.Code, e.Message, e.RequestID)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}
