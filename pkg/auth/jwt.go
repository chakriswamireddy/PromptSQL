package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// JWTClaims are the identity-only claims embedded in the access token.
// Roles, permissions, and attributes are NEVER included; they are resolved server-side.
type JWTClaims struct {
	jwt.RegisteredClaims
	// Tenant is the tenant UUID. Gateway cross-checks URL path slug against this.
	Tenant    string   `json:"tenant"`
	SessionID string   `json:"session_id"`
	// AMR lists authentication methods (e.g. ["pwd","totp"]).
	AMR   []string `json:"amr,omitempty"`
	MFAAt int64    `json:"mfa_at,omitempty"`
}

// JWTConfig holds Ed25519 key material and validation parameters.
type JWTConfig struct {
	PrivateKey ed25519.PrivateKey // required for signing (gateway only)
	PublicKey  ed25519.PublicKey  // required for verification
	Issuer     string
	Audience   string
	AccessTTL  time.Duration // default 10 min
	ClockSkew  time.Duration // default 60 s
}

// JWTService signs and verifies Ed25519 (EdDSA) JWTs.
// The algorithm allowlist is enforced: only "EdDSA" is accepted.
type JWTService struct {
	cfg    JWTConfig
	parser *jwt.Parser
}

// NewJWTService returns a JWTService. Both PrivateKey and PublicKey must be set
// when signing is required; PublicKey alone is sufficient for verify-only services.
func NewJWTService(cfg JWTConfig) *JWTService {
	if cfg.AccessTTL == 0 {
		cfg.AccessTTL = 10 * time.Minute
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 60 * time.Second
	}
	return &JWTService{
		cfg: cfg,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"EdDSA"}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithLeeway(cfg.ClockSkew),
			jwt.WithExpirationRequired(),
		),
	}
}

// Sign mints a new access token for the given subject. The jti is a fresh UUIDv7.
func (s *JWTService) Sign(userID, tenantID, sessionID string, amr []string, mfaAt int64) (string, error) {
	if s.cfg.PrivateKey == nil {
		return "", errors.New("jwt: no private key configured for signing")
	}
	now := time.Now()
	jti, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("jwt: generate jti: %w", err)
	}
	claims := JWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.Issuer,
			Subject:   userID,
			Audience:  jwt.ClaimStrings{s.cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.cfg.AccessTTL)),
			ID:        jti.String(),
		},
		Tenant:    tenantID,
		SessionID: sessionID,
		AMR:       amr,
		MFAAt:     mfaAt,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(s.cfg.PrivateKey)
}

// Verify parses and validates a token string. It enforces the EdDSA allowlist,
// iss, aud, exp, iat checks. The caller must separately replay-check the jti.
func (s *JWTService) Verify(tokenString string) (*JWTClaims, error) {
	claims := &JWTClaims{}
	_, err := s.parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("jwt: unexpected alg %v — only EdDSA accepted", t.Header["alg"])
		}
		return s.cfg.PublicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("jwt: verify: %w", err)
	}
	return claims, nil
}

// JWKSPayload returns a JSON Web Key Set for the current public key.
// Downstream services cache this; key rotation adds a second entry.
func (s *JWTService) JWKSPayload() ([]byte, error) {
	pubBytes := []byte(s.cfg.PublicKey)
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"use": "sig",
		"alg": "EdDSA",
		"x":   base64.RawURLEncoding.EncodeToString(pubBytes),
		"kid": "current",
	}
	return json.Marshal(map[string]any{"keys": []any{jwk}})
}

// ParseEd25519PrivateKeyB64 decodes a base64-encoded Ed25519 private key seed
// or full 64-byte key and returns the private + public key pair.
func ParseEd25519PrivateKeyB64(b64 string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		// Try raw URL encoding (no padding)
		raw, err = base64.RawURLEncoding.DecodeString(b64)
		if err != nil {
			return nil, nil, fmt.Errorf("jwt: decode private key: %w", err)
		}
	}
	switch len(raw) {
	case ed25519.SeedSize:
		priv := ed25519.NewKeyFromSeed(raw)
		return priv, priv.Public().(ed25519.PublicKey), nil
	case ed25519.PrivateKeySize:
		priv := ed25519.PrivateKey(raw)
		return priv, priv.Public().(ed25519.PublicKey), nil
	}
	return nil, nil, fmt.Errorf("jwt: invalid key size %d", len(raw))
}

// GenerateEd25519KeyB64 generates a new Ed25519 keypair and returns both
// as standard base64 strings (private, public).
func GenerateEd25519KeyB64() (privB64, pubB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", fmt.Errorf("jwt: generate key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv),
		base64.StdEncoding.EncodeToString(pub),
		nil
}
