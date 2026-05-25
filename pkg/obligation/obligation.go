// Package obligation signs and verifies step-up MFA obligation tokens.
// Tokens are short-lived HMAC-SHA256 signed blobs shared between the PDP,
// PEP proxy, and the step-up auth handler.
package obligation

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const DefaultTTL = 5 * time.Minute

// Token carries the step-up obligation payload.
type Token struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tid"`
	UserID      string    `json:"uid"`
	SessionJTI  string    `json:"jti"`
	Type        string    `json:"typ"`
	Reason      string    `json:"reason,omitempty"`
	RiskScore   int       `json:"rs,omitempty"`
	IssuedAt    time.Time `json:"iat"`
	ExpiresAt   time.Time `json:"exp"`
}

// Satisfied reports whether the token's MFA requirement is met by mfaAt.
func (t *Token) Satisfied(mfaAt time.Time) bool {
	return !mfaAt.IsZero() && mfaAt.After(t.IssuedAt)
}

// Expired reports whether the obligation token has expired.
func (t *Token) Expired() bool {
	return time.Now().UTC().After(t.ExpiresAt)
}

// Service signs and verifies obligation tokens with HMAC-SHA256.
type Service struct {
	key []byte
	ttl time.Duration
}

// New creates a Service. key must be at least 32 bytes.
func New(key []byte, ttl time.Duration) (*Service, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("obligation: signing key must be >= 32 bytes, got %d", len(key))
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{key: key, ttl: ttl}, nil
}

// Issue creates a signed obligation token for the given user and session.
// The returned string is the wire format to embed in HTTP 401 bodies or PG errors.
func (s *Service) Issue(tenantID, userID, sessionJTI, reason string, riskScore int) (string, *Token, error) {
	id, err := newID()
	if err != nil {
		return "", nil, fmt.Errorf("obligation: generate id: %w", err)
	}
	now := time.Now().UTC()
	tok := &Token{
		ID:         id,
		TenantID:   tenantID,
		UserID:     userID,
		SessionJTI: sessionJTI,
		Type:       "require_mfa",
		Reason:     reason,
		RiskScore:  riskScore,
		IssuedAt:   now,
		ExpiresAt:  now.Add(s.ttl),
	}
	encoded, err := s.encode(tok)
	if err != nil {
		return "", nil, err
	}
	return encoded, tok, nil
}

// Verify decodes and authenticates an obligation token string.
// Returns an error if the signature is invalid or the token is expired.
func (s *Service) Verify(tokenStr string) (*Token, error) {
	parts := strings.SplitN(tokenStr, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("obligation: malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("obligation: decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("obligation: decode sig: %w", err)
	}
	expected := s.sign(payload)
	if !hmac.Equal(sig, expected) {
		return nil, fmt.Errorf("obligation: signature mismatch")
	}
	var tok Token
	if err := json.Unmarshal(payload, &tok); err != nil {
		return nil, fmt.Errorf("obligation: unmarshal payload: %w", err)
	}
	if tok.Expired() {
		return nil, fmt.Errorf("obligation: token expired at %s", tok.ExpiresAt.Format(time.RFC3339))
	}
	return &tok, nil
}

func (s *Service) encode(tok *Token) (string, error) {
	payload, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("obligation: marshal: %w", err)
	}
	sig := s.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Service) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return mac.Sum(nil)
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
