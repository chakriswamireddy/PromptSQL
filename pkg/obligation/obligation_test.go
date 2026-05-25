package obligation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/governance-platform/pkg/obligation"
)

func TestRoundTrip(t *testing.T) {
	svc, err := obligation.New([]byte("aaaabbbbccccddddeeeeffffgggghhhh"), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tokenStr, issued, err := svc.Issue("tenant1", "user1", "jti-abc", "risk_threshold", 78)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := svc.Verify(tokenStr)
	if err != nil {
		t.Fatal(err)
	}
	if verified.TenantID != issued.TenantID {
		t.Errorf("tenant mismatch: got %s want %s", verified.TenantID, issued.TenantID)
	}
	if verified.RiskScore != 78 {
		t.Errorf("risk score mismatch: got %d want 78", verified.RiskScore)
	}
}

func TestExpiry(t *testing.T) {
	svc, _ := obligation.New([]byte("aaaabbbbccccddddeeeeffffgggghhhh"), time.Millisecond)
	tokenStr, _, _ := svc.Issue("t1", "u1", "jti", "test", 50)
	time.Sleep(2 * time.Millisecond)
	_, err := svc.Verify(tokenStr)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestTamperedSignature(t *testing.T) {
	svc, _ := obligation.New([]byte("aaaabbbbccccddddeeeeffffgggghhhh"), time.Hour)
	tokenStr, _, _ := svc.Issue("t1", "u1", "jti", "test", 80)
	parts := strings.SplitN(tokenStr, ".", 2)
	tampered := parts[0] + ".AAAA"
	_, err := svc.Verify(tampered)
	if err == nil {
		t.Fatal("expected error for tampered signature, got nil")
	}
}

func TestKeyTooShort(t *testing.T) {
	_, err := obligation.New([]byte("short"), time.Minute)
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestSatisfied(t *testing.T) {
	svc, _ := obligation.New([]byte("aaaabbbbccccddddeeeeffffgggghhhh"), time.Hour)
	_, tok, _ := svc.Issue("t1", "u1", "jti", "test", 75)

	before := tok.IssuedAt.Add(-time.Second)
	if tok.Satisfied(before) {
		t.Error("should not be satisfied by mfa before issuance")
	}
	after := tok.IssuedAt.Add(time.Second)
	if !tok.Satisfied(after) {
		t.Error("should be satisfied by mfa after issuance")
	}
}
