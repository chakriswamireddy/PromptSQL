// Package auth defines the SessionContext propagated through every service call,
// the Ed25519 JWT service, HMAC service-to-service trust, and JTI replay protection.
// This is the single source of truth for auth types; downstream services import it.
package auth

import "time"

// DeviceTrust classifies the trust level of the originating device.
type DeviceTrust string

// NetworkTrust classifies the network path of the request.
type NetworkTrust string

// SubjectKind distinguishes human users from machine callers.
type SubjectKind string

const (
	DeviceTrustManaged DeviceTrust = "managed"
	DeviceTrustBYOD    DeviceTrust = "byod"
	DeviceTrustUnknown DeviceTrust = "unknown"

	NetworkTrustCorp   NetworkTrust = "corp"
	NetworkTrustVPN    NetworkTrust = "vpn"
	NetworkTrustPublic NetworkTrust = "public"

	SubjectKindUser    SubjectKind = "user"
	SubjectKindService SubjectKind = "service"
	SubjectKindAPIKey  SubjectKind = "apikey"
)

// SessionAttributes holds per-user ABAC attributes resolved server-side.
type SessionAttributes struct {
	Department     string       `json:"department,omitempty"`
	CampusID       string       `json:"campusId,omitempty"`
	Region         string       `json:"region,omitempty"`
	ClearanceLevel int          `json:"clearanceLevel,omitempty"`
	MFASince       *time.Time   `json:"mfaSince,omitempty"`
	DeviceTrust    DeviceTrust  `json:"deviceTrust"`
	NetworkTrust   NetworkTrust `json:"networkTrust"`
}

// SessionContext is the authoritative, server-constructed session blob attached
// to every request context. Roles and attributes are NEVER sourced from JWT claims;
// they are resolved freshly from the database per request (60 s cache).
type SessionContext struct {
	UserID       string            `json:"userId"`
	TenantID     string            `json:"tenantId"`
	SessionID    string            `json:"sessionId"`
	SubjectKind  SubjectKind       `json:"subjectKind"`
	Roles        []string          `json:"roles"`
	Attributes   SessionAttributes `json:"attributes"`
	RequestID    string            `json:"requestId"`
	TraceID      string            `json:"traceId"`
	ParentSpanID string            `json:"parentSpanId,omitempty"`
	IsBreakGlass bool              `json:"isBreakGlass"`
	// RiskScore is nil until Phase 13 populates it.
	RiskScore *int `json:"riskScore,omitempty"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// AMR and MFAAt are sourced from the JWT and carried for obligation checks.
	AMR   []string   `json:"amr,omitempty"`
	MFAAt *time.Time `json:"mfaAt,omitempty"`
}

// GetUserID implements db.SessionData.
func (s *SessionContext) GetUserID() string { return s.UserID }

// GetTenantID implements db.SessionData.
func (s *SessionContext) GetTenantID() string { return s.TenantID }

// GetSessionID implements db.SessionData.
func (s *SessionContext) GetSessionID() string { return s.SessionID }

// GetRequestID implements db.SessionData.
func (s *SessionContext) GetRequestID() string { return s.RequestID }

// GetTraceID implements db.SessionData.
func (s *SessionContext) GetTraceID() string { return s.TraceID }

// GetBreakGlass implements db.SessionData.
func (s *SessionContext) GetBreakGlass() bool { return s.IsBreakGlass }
