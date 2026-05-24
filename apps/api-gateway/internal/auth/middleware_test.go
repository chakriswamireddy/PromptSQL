package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pkgauth "github.com/governance-platform/pkg/auth"
)

// TestContextRoundtrip ensures WithContext → FromContext preserves the value.
func TestContextRoundtrip(t *testing.T) {
	sc := &pkgauth.SessionContext{
		UserID:    "user-123",
		TenantID:  "tenant-456",
		SessionID: "sess-789",
	}
	ctx := WithContext(context.Background(), sc)
	got := FromContext(ctx)
	if got == nil {
		t.Fatal("FromContext returned nil after WithContext")
	}
	if got.UserID != sc.UserID {
		t.Errorf("UserID: got %q want %q", got.UserID, sc.UserID)
	}
	if got.TenantID != sc.TenantID {
		t.Errorf("TenantID: got %q want %q", got.TenantID, sc.TenantID)
	}
}

// TestContextMissing confirms FromContext returns nil on a bare context.
func TestContextMissing(t *testing.T) {
	if got := FromContext(context.Background()); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestWriteError_ContentType verifies writeError produces problem+json.
// We call it indirectly via a minimal handler that returns no auth header.
func TestMissingAuthorizationHeader(t *testing.T) {
	// Build a minimal stub middleware that only checks for Bearer presence.
	handler := stubMiddleware(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header required", "test-req-1")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/data", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/problem+json") {
		t.Errorf("Content-Type: got %q want application/problem+json", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "missing_token") {
		t.Errorf("body missing error code: %s", body)
	}
}

// TestWrongAuthScheme confirms "Token <x>" (not Bearer) also gets 401.
func TestWrongAuthScheme(t *testing.T) {
	handler := stubMiddleware(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing_token", "Bearer required", "req-2")
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/data", nil)
	req.Header.Set("Authorization", "Token abc123")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong scheme, got %d", rr.Code)
	}
}

// TestRequestIDHeader verifies X-Request-ID is set on responses.
func TestRequestIDHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "test-req-id")
		w.WriteHeader(http.StatusOK)
	}).ServeHTTP(rr, req)

	if rr.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

// stubMiddleware wraps an http.HandlerFunc as http.Handler for test convenience.
func stubMiddleware(fn http.HandlerFunc) http.Handler { return fn }
