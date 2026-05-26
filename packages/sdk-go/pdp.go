package governance

import (
	"context"
	"net/http"
)

// PDPClient calls the Policy Decision Point.
type PDPClient struct{ c *Client }

// DecideRequest is the input to a policy decision.
type DecideRequest struct {
	TenantID     string                 `json:"tenant_id"`
	Subject      SubjectContext         `json:"subject"`
	Resource     ResourceContext        `json:"resource"`
	Action       string                 `json:"action"`
	Environment  map[string]interface{} `json:"environment,omitempty"`
}

// SubjectContext identifies the actor.
type SubjectContext struct {
	UserID string            `json:"user_id"`
	Roles  []string          `json:"roles"`
	Attrs  map[string]string `json:"attrs,omitempty"`
}

// ResourceContext identifies the target resource.
type ResourceContext struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// DecideResponse contains the PDP verdict.
type DecideResponse struct {
	Decision    string            `json:"decision"`
	Obligations []Obligation      `json:"obligations,omitempty"`
	ColumnMasks map[string]string `json:"column_masks,omitempty"`
	RowFilter   string            `json:"row_filter,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	TraceID     string            `json:"trace_id,omitempty"`
}

// Obligation is an action required alongside an allow decision.
type Obligation struct {
	Type    string                 `json:"type"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

// Decide evaluates a single policy decision.
func (p *PDPClient) Decide(ctx context.Context, req DecideRequest) (*DecideResponse, error) {
	var resp DecideResponse
	if err := p.c.do(ctx, http.MethodPost, "/v1/pdp/decide", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BulkDecideRequest bundles multiple decisions in one call.
type BulkDecideRequest struct {
	TenantID string          `json:"tenant_id"`
	Subject  SubjectContext  `json:"subject"`
	Requests []DecideRequest `json:"requests"`
}

// BulkDecide evaluates multiple policy decisions in one round-trip.
func (p *PDPClient) BulkDecide(ctx context.Context, req BulkDecideRequest) ([]DecideResponse, error) {
	var resp struct {
		Results []DecideResponse `json:"results"`
	}
	if err := p.c.do(ctx, http.MethodPost, "/v1/pdp/bulk-decide", req, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// Explain returns a human-readable explanation of a policy decision.
func (p *PDPClient) Explain(ctx context.Context, req DecideRequest) (string, error) {
	var resp struct {
		Explanation string `json:"explanation"`
	}
	if err := p.c.do(ctx, http.MethodPost, "/v1/pdp/explain", req, &resp); err != nil {
		return "", err
	}
	return resp.Explanation, nil
}
