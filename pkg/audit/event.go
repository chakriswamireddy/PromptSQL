// Package audit provides a fire-and-forget audit event producer.
// Events are batched locally and flushed to Kafka every 500 ms or 1 MB,
// whichever comes first. On Kafka outage the producer spools to local disk
// (bounded to DiskBufferMaxBytes) and replays on reconnection.
package audit

import (
	"time"

	"github.com/google/uuid"
)

// Schema version embedded in every event payload.
const SchemaV1 = "v1"

// PolicyAction enumerates audit actions on policies.
type PolicyAction string

const (
	PolicyCreate   PolicyAction = "policy.create"
	PolicyActivate PolicyAction = "policy.activate"
	PolicyArchive  PolicyAction = "policy.archive"
	PolicySimulate PolicyAction = "policy.simulate"
	PolicyUpdate   PolicyAction = "policy.update"
	PolicyReview   PolicyAction = "policy.review"
)

// AccessDecision is the outcome of a PDP evaluation.
type AccessDecision string

const (
	DecisionAllow AccessDecision = "allow"
	DecisionDeny  AccessDecision = "deny"
	DecisionError AccessDecision = "error"
)

// PolicyEvent records a mutation to a policy or policy-set.
type PolicyEvent struct {
	EventID     string       `json:"event_id"`
	Schema      string       `json:"schema"`
	Service     string       `json:"service"`
	EventTime   time.Time    `json:"event_time"`
	TenantID    string       `json:"tenant_id"`
	ActorID     string       `json:"actor_id"`
	ActorToken  string       `json:"actor_token,omitempty"` // HMAC-tokenized
	Action      PolicyAction `json:"action"`
	PolicyID    string       `json:"policy_id"`
	BeforeState any          `json:"before_state,omitempty"`
	AfterState  any          `json:"after_state,omitempty"`
	Metadata    EventMeta    `json:"metadata"`
}

// AccessEvent records a PDP access decision.
type AccessEvent struct {
	EventID      string         `json:"event_id"`
	Schema       string         `json:"schema"`
	Service      string         `json:"service"`
	EventTime    time.Time      `json:"event_time"`
	TenantID     string         `json:"tenant_id"`
	UserID       string         `json:"user_id"`
	ActorToken   string         `json:"actor_token,omitempty"` // HMAC-tokenized
	DataSourceID string         `json:"data_source_id"`
	Resource     string         `json:"resource"`
	Action       string         `json:"action"`
	Decision     AccessDecision `json:"decision"`
	Reason       string         `json:"reason,omitempty"`
	RowCount     int64          `json:"row_count,omitempty"`
	QueryHash    string         `json:"query_hash,omitempty"`
	DurationMs   int64          `json:"duration_ms"`
	RiskScore    float64        `json:"risk_score,omitempty"`
	BreakGlass   bool           `json:"break_glass,omitempty"`
	PolicyVersion string        `json:"policy_version,omitempty"`
	Metadata     EventMeta      `json:"metadata"`
}

// SystemEvent records platform-level operations (deploys, key rotations, etc.).
type SystemEvent struct {
	EventID   string    `json:"event_id"`
	Schema    string    `json:"schema"`
	Service   string    `json:"service"`
	EventTime time.Time `json:"event_time"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Action    string    `json:"action"`
	Detail    any       `json:"detail,omitempty"`
	Metadata  EventMeta `json:"metadata"`
}

// EventMeta is common metadata carried by every event.
type EventMeta struct {
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	IP        string `json:"ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	MFAAt     int64  `json:"mfa_at,omitempty"`
}

// newEventID generates a UUIDv4 event ID (UUIDv7 not yet in stdlib).
func newEventID() string {
	return uuid.New().String()
}
