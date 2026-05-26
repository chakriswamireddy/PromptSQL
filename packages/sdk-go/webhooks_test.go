package governance_test

import (
	"testing"

	governance "github.com/org/platform/sdk-go"
)

func TestVerifySignature_valid(t *testing.T) {
	payload := []byte(`{"event":"policy.created"}`)
	secret := "test-secret-abc123"

	// Compute expected signature.
	import_crypto_hmac "crypto/hmac"
	import_sha256 "crypto/sha256"
	import_hex "encoding/hex"
	mac := import_crypto_hmac.New(import_sha256.New, []byte(secret))
	mac.Write(payload)
	sig := "sha256=" + import_hex.EncodeToString(mac.Sum(nil))

	if err := governance.VerifySignature(payload, sig, secret); err != nil {
		t.Fatalf("expected valid signature, got: %v", err)
	}
}

func TestVerifySignature_invalid(t *testing.T) {
	payload := []byte(`{"event":"policy.created"}`)
	if err := governance.VerifySignature(payload, "sha256=badhex", "secret"); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestVerifySignature_badFormat(t *testing.T) {
	if err := governance.VerifySignature([]byte("x"), "noequalssign", "s"); err == nil {
		t.Fatal("expected format error")
	}
}
