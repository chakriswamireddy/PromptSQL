package server_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	pkgauth "github.com/governance-platform/pkg/auth"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/governance-platform/pdp/internal/cache"
	"github.com/governance-platform/pdp/internal/invalidation"
	"github.com/governance-platform/pdp/internal/server"
	"github.com/governance-platform/pdp/internal/store"
	pkgdb "github.com/governance-platform/pkg/db"
	"github.com/governance-platform/pkg/logging"
	"github.com/governance-platform/policy-engine/engine"
)

// stubStore is a test double for store.Store that returns hand-crafted policies.
type stubStore struct {
	policies []engine.Policy
	version  int64
}

func (s *stubStore) LoadActivePolicies(_ context.Context, _ string) ([]engine.Policy, []error, error) {
	return s.policies, nil, nil
}
func (s *stubStore) PolicySetVersion(_ context.Context, _ string) (int64, error) {
	return s.version, nil
}

// realStore wraps *store.Store for the interface.
type realStoreAdapter struct{ inner *store.Store }

func (r *realStoreAdapter) LoadActivePolicies(ctx context.Context, tenantID string) ([]engine.Policy, []error, error) {
	return r.inner.LoadActivePolicies(ctx, tenantID)
}
func (r *realStoreAdapter) PolicySetVersion(ctx context.Context, tenantID string) (int64, error) {
	return r.inner.PolicySetVersion(ctx, tenantID)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func testHMAC(t *testing.T) *pkgauth.HMACService {
	t.Helper()
	svc, err := pkgauth.NewHMACService(map[string][]byte{"test": []byte("test-secret-32-bytes-long-padded!!")})
	if err != nil {
		t.Fatalf("hmac: %v", err)
	}
	return svc
}

func signSession(t *testing.T, hmacSvc *pkgauth.HMACService, sc *pkgauth.SessionContext) (ctxBytes, sigBytes []byte, keyID string) {
	t.Helper()
	sc.IssuedAt = time.Now()
	sc.ExpiresAt = time.Now().Add(10 * time.Minute)
	ctxB64, sigB64, kid, err := hmacSvc.Sign(sc)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ctxBytes, _ = base64.StdEncoding.DecodeString(ctxB64)
	sigBytes, _ = base64.StdEncoding.DecodeString(sigB64)
	return ctxBytes, sigBytes, kid
}

func baseSessionContext(tenantID string) *pkgauth.SessionContext {
	return &pkgauth.SessionContext{
		UserID:   "user-unit-test",
		TenantID: tenantID,
		SessionID: "sess-1",
		Attributes: pkgauth.SessionAttributes{Department: "finance"},
		Roles: []string{"analyst"},
	}
}

// buildServer creates a Server backed by a stub store (no DB required).
func buildServer(t *testing.T, policies []engine.Policy) (*server.Server, *pkgauth.HMACService) {
	t.Helper()
	hmacSvc := testHMAC(t)
	decisionCache := cache.New(nil) // no Redis in unit tests
	versions := invalidation.NewVersionStore()
	sub := invalidation.New(nil, versions, nil, logging.New("pdp-test", "test", "test"))

	// We can't easily inject a stubStore through the current server.Config because
	// Config.Store is *store.Store (concrete). For unit tests, we test the decision
	// logic via the policy-engine package directly; integration tests use a real DB.
	// This test validates the server's HMAC rejection path.
	srv := server.New(server.Config{
		Store:    store.New(pkgdb.New(nil)), // nil pool → will fail on DB call
		Cache:    decisionCache,
		HMAC:     hmacSvc,
		Sub:      sub,
		Versions: versions,
		Log:      logging.New("pdp-test", "test", "test"),
	})
	return srv, hmacSvc
}

// TestDecide_InvalidHMAC verifies that a tampered session context is rejected.
func TestDecide_InvalidHMAC(t *testing.T) {
	srv, _ := buildServer(t, nil)
	ctx := context.Background()

	sc := baseSessionContext("tenant-test")
	scJSON, _ := json.Marshal(sc)
	req := &pdpv1.DecideRequest{
		SubjectSessionContext: scJSON,
		SubjectSessionSig:     []byte("invalid-sig"),
		SubjectSessionKeyId:   "test",
		Action:                "SELECT",
		Resource:              "pg://ds1/public/users",
	}
	_, err := srv.Decide(ctx, req)
	if err == nil {
		t.Fatal("expected error for invalid HMAC")
	}
}

// TestValidate_ValidDSL checks the Validate endpoint returns valid=true for a correct DSL.
func TestValidate_ValidDSL(t *testing.T) {
	srv, _ := buildServer(t, nil)
	ctx := context.Background()

	conditions := []byte(`{"field":"subject.department","op":"eq","value":"finance"}`)
	resp, err := srv.Validate(ctx, &pdpv1.ValidateRequest{
		TenantId:   "tenant-test",
		Conditions: conditions,
	})
	if err != nil {
		t.Fatalf("unexpected gRPC error: %v", err)
	}
	if !resp.Valid {
		t.Fatalf("expected valid=true, got errors: %v", resp.Errors)
	}
}

// TestValidate_InvalidDSL checks that a bad operator is caught.
func TestValidate_InvalidDSL(t *testing.T) {
	srv, _ := buildServer(t, nil)
	ctx := context.Background()

	conditions := []byte(`{"field":"subject.department","op":"INVALID","value":"finance"}`)
	resp, err := srv.Validate(ctx, &pdpv1.ValidateRequest{
		TenantId:   "tenant-test",
		Conditions: conditions,
	})
	if err != nil {
		t.Fatalf("unexpected gRPC error: %v", err)
	}
	if resp.Valid {
		t.Fatal("expected valid=false for invalid operator")
	}
	if len(resp.Errors) == 0 {
		t.Fatal("expected at least one error message")
	}
}

// TestValidate_MaxDepth ensures deep nesting is caught.
func TestValidate_MaxDepth(t *testing.T) {
	srv, _ := buildServer(t, nil)
	ctx := context.Background()

	// Build 6 levels of "not" nesting (exceeds max depth 5).
	conditions := []byte(`{"not":{"not":{"not":{"not":{"not":{"not":{"field":"subject.x","op":"eq","value":"v"}}}}}}}`)
	resp, _ := srv.Validate(ctx, &pdpv1.ValidateRequest{
		TenantId:   "tenant-test",
		Conditions: conditions,
	})
	if resp.Valid {
		t.Fatal("expected valid=false for over-depth condition")
	}
}
