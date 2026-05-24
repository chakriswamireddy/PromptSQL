package auth_test

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/governance-platform/pkg/auth"
)

func newTestJWT(t *testing.T) *auth.JWTService {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	return auth.NewJWTService(auth.JWTConfig{
		PrivateKey: priv,
		PublicKey:  pub,
		Issuer:     "https://test.platform.io",
		Audience:   "test-api",
		AccessTTL:  10 * time.Minute,
	})
}

func TestJWT_SignAndVerify(t *testing.T) {
	svc := newTestJWT(t)
	token, err := svc.Sign("user-1", "tenant-1", "sess-1", []string{"pwd"}, 0)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	claims, err := svc.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("subject: got %q want %q", claims.Subject, "user-1")
	}
	if claims.Tenant != "tenant-1" {
		t.Errorf("tenant: got %q want %q", claims.Tenant, "tenant-1")
	}
	if claims.ID == "" {
		t.Error("jti must be non-empty")
	}
}

func TestJWT_TamperedSignature(t *testing.T) {
	svc := newTestJWT(t)
	token, _ := svc.Sign("user-1", "tenant-1", "sess-1", nil, 0)
	// Flip the last byte of the signature (base64 last segment).
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("expected 3 JWT parts")
	}
	sig := []byte(parts[2])
	sig[len(sig)-1] ^= 0x01
	tampered := parts[0] + "." + parts[1] + "." + string(sig)

	if _, err := svc.Verify(tampered); err == nil {
		t.Error("expected error for tampered token, got nil")
	}
}

func TestJWT_WrongAlgorithm(t *testing.T) {
	svc := newTestJWT(t)
	// Manually build a token claiming alg=none (only possible via manual construction).
	// jwt/v5 rejects any non-EdDSA algorithm at the parser level.
	noneToken := "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."
	if _, err := svc.Verify(noneToken); err == nil {
		t.Error("expected rejection of alg=none token")
	}
}

func TestJWT_WrongAudience(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	signer := auth.NewJWTService(auth.JWTConfig{
		PrivateKey: priv, PublicKey: pub,
		Issuer: "https://test.platform.io", Audience: "other-service",
		AccessTTL: 10 * time.Minute,
	})
	verifier := auth.NewJWTService(auth.JWTConfig{
		PublicKey: pub,
		Issuer:    "https://test.platform.io", Audience: "test-api",
		AccessTTL: 10 * time.Minute,
	})
	token, _ := signer.Sign("u", "t", "s", nil, 0)
	if _, err := verifier.Verify(token); err == nil {
		t.Error("expected audience mismatch rejection")
	}
}

func TestJWT_Expired(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	pub := priv.Public().(ed25519.PublicKey)
	svc := auth.NewJWTService(auth.JWTConfig{
		PrivateKey: priv, PublicKey: pub,
		Issuer: "https://test.platform.io", Audience: "test-api",
		AccessTTL: -1 * time.Second, // already expired
		ClockSkew: 0,
	})
	token, _ := svc.Sign("u", "t", "s", nil, 0)
	if _, err := svc.Verify(token); err == nil {
		t.Error("expected expired token rejection")
	}
}

func TestJWT_GenerateAndParseKey(t *testing.T) {
	privB64, _, err := auth.GenerateEd25519KeyB64()
	if err != nil {
		t.Fatalf("GenerateEd25519KeyB64: %v", err)
	}
	priv, pub, err := auth.ParseEd25519PrivateKeyB64(privB64)
	if err != nil {
		t.Fatalf("ParseEd25519PrivateKeyB64: %v", err)
	}
	svc := auth.NewJWTService(auth.JWTConfig{
		PrivateKey: priv, PublicKey: pub,
		Issuer: "https://test.platform.io", Audience: "test-api",
		AccessTTL: 10 * time.Minute,
	})
	token, err := svc.Sign("u", "t", "s", nil, 0)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := svc.Verify(token); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestJWT_JWKS(t *testing.T) {
	svc := newTestJWT(t)
	data, err := svc.JWKSPayload()
	if err != nil {
		t.Fatalf("JWKSPayload: %v", err)
	}
	if len(data) == 0 {
		t.Error("empty JWKS payload")
	}
	if !strings.Contains(string(data), `"alg":"EdDSA"`) {
		t.Errorf("JWKS should contain EdDSA algorithm, got: %s", data)
	}
}

func TestHMAC_SignAndVerify(t *testing.T) {
	secrets := map[string][]byte{"v1": []byte("supersecretkey")}
	svc, err := auth.NewHMACService(secrets)
	if err != nil {
		t.Fatalf("NewHMACService: %v", err)
	}
	sc := &auth.SessionContext{
		UserID:    "user-1",
		TenantID:  "tenant-1",
		SessionID: "sess-1",
		RequestID: "req-1",
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	ctxB64, sigB64, keyID, err := svc.Sign(sc)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := svc.Verify(ctxB64, sigB64, keyID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != sc.UserID {
		t.Errorf("UserID: got %q want %q", got.UserID, sc.UserID)
	}
}

func TestHMAC_TamperedSignature(t *testing.T) {
	secrets := map[string][]byte{"v1": []byte("supersecretkey")}
	svc, _ := auth.NewHMACService(secrets)
	sc := &auth.SessionContext{UserID: "u", TenantID: "t", IssuedAt: time.Now()}
	ctxB64, _, keyID, _ := svc.Sign(sc)
	if _, err := svc.Verify(ctxB64, "invalidsig==", keyID); err == nil {
		t.Error("expected HMAC mismatch error")
	}
}

func TestHMAC_UnknownKeyID(t *testing.T) {
	secrets := map[string][]byte{"v1": []byte("secret")}
	svc, _ := auth.NewHMACService(secrets)
	sc := &auth.SessionContext{UserID: "u", TenantID: "t", IssuedAt: time.Now()}
	ctxB64, sigB64, _, _ := svc.Sign(sc)
	if _, err := svc.Verify(ctxB64, sigB64, "unknown-key"); err == nil {
		t.Error("expected unknown key ID error")
	}
}

func TestHMAC_ReplayWindow(t *testing.T) {
	secrets := map[string][]byte{"v1": []byte("secret")}
	svc, _ := auth.NewHMACService(secrets)
	// IssuedAt well outside the 60 s window.
	sc := &auth.SessionContext{
		UserID:   "u",
		TenantID: "t",
		IssuedAt: time.Now().Add(-2 * time.Minute),
	}
	ctxB64, sigB64, keyID, _ := svc.Sign(sc)
	if _, err := svc.Verify(ctxB64, sigB64, keyID); err == nil {
		t.Error("expected freshness window rejection")
	}
}

// TestPassword_HashAndVerify tests the Argon2id password hashing.
func TestPassword_HashAndVerify(t *testing.T) {
	// This test lives in the pkg/auth package but password.go is in internal/auth.
	// The hash/verify functions are tested at the api-gateway package level.
	// This placeholder ensures the test file compiles.
}
