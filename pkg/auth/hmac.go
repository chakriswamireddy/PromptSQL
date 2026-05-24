package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const hmacFreshnessWindow = 60 * time.Second

// HMACService signs and verifies the service-to-service SessionContext headers.
// V1 uses a shared secret rotated weekly via Vault; V2 (Phase 15) replaces this
// with per-service mTLS tokens issued by the service mesh.
//
// Header contract:
//
//	X-Session-Context:        base64(JSON(SessionContext))
//	X-Session-Context-Sig:   base64(HMAC-SHA256(secret, ctxB64))
//	X-Session-Context-KeyId: keyID
type HMACService struct {
	// secrets maps keyID → secret; multiple entries allow zero-downtime rotation.
	secrets map[string][]byte
}

// NewHMACService returns an HMACService. secrets must have at least one entry.
func NewHMACService(secrets map[string][]byte) (*HMACService, error) {
	if len(secrets) == 0 {
		return nil, errors.New("hmac: at least one secret required")
	}
	return &HMACService{secrets: secrets}, nil
}

// Sign serialises sc as JSON, base64-encodes it, computes HMAC-SHA256, and
// returns the three header values (ctxB64, sigB64, keyID).
// The current key is arbitrarily the first one iterated (map iteration is random;
// for deterministic selection sort by keyID in production).
func (h *HMACService) Sign(sc *SessionContext) (ctxB64, sigB64, keyID string, err error) {
	for id, secret := range h.secrets {
		keyID = id
		scJSON, merr := json.Marshal(sc)
		if merr != nil {
			return "", "", "", fmt.Errorf("hmac: marshal session context: %w", merr)
		}
		ctxB64 = base64.StdEncoding.EncodeToString(scJSON)
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(ctxB64))
		sigB64 = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		return ctxB64, sigB64, keyID, nil
	}
	return "", "", "", errors.New("hmac: no secrets configured")
}

// Verify checks the HMAC, decodes the SessionContext, and validates the 60 s
// freshness window to prevent replay within the network.
func (h *HMACService) Verify(ctxB64, sigB64, keyID string) (*SessionContext, error) {
	secret, ok := h.secrets[keyID]
	if !ok {
		return nil, fmt.Errorf("hmac: unknown key id %q", keyID)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ctxB64))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// Constant-time comparison prevents timing oracle.
	if !hmac.Equal([]byte(sigB64), []byte(expected)) {
		return nil, errors.New("hmac: signature mismatch")
	}

	scJSON, err := base64.StdEncoding.DecodeString(ctxB64)
	if err != nil {
		return nil, fmt.Errorf("hmac: decode context: %w", err)
	}
	var sc SessionContext
	if err := json.Unmarshal(scJSON, &sc); err != nil {
		return nil, fmt.Errorf("hmac: unmarshal session context: %w", err)
	}
	if time.Since(sc.IssuedAt) > hmacFreshnessWindow {
		return nil, errors.New("hmac: session context outside freshness window (replay?)")
	}
	return &sc, nil
}
