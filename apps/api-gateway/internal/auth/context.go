// Package auth implements the API Gateway authentication layer:
// JWT verification, SessionContext construction, HMAC propagation,
// password auth, TOTP MFA, and refresh-token lifecycle.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	pkgauth "github.com/governance-platform/pkg/auth"
)

type contextKey struct{ name string }

var sessionContextKey = contextKey{"session"}

// WithContext stores sc in ctx.
func WithContext(ctx context.Context, sc *pkgauth.SessionContext) context.Context {
	return context.WithValue(ctx, sessionContextKey, sc)
}

// FromContext retrieves the SessionContext from ctx.
// Returns nil if not present.
func FromContext(ctx context.Context) *pkgauth.SessionContext {
	sc, _ := ctx.Value(sessionContextKey).(*pkgauth.SessionContext)
	return sc
}

// MustFromContext retrieves the SessionContext or panics.
// Use only inside handlers that are guarded by the auth middleware.
func MustFromContext(ctx context.Context) *pkgauth.SessionContext {
	sc := FromContext(ctx)
	if sc == nil {
		panic("auth: SessionContext missing from context — handler not behind middleware")
	}
	return sc
}

// writeError writes an RFC 7807 problem+json error response.
func writeError(w http.ResponseWriter, status int, code, msg, requestID string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{
		"code":       code,
		"message":    msg,
		"request_id": requestID,
	})
	_, _ = w.Write(body)
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Nothing we can do; headers already sent.
		_ = fmt.Errorf("writeJSON encode: %w", err)
	}
}
