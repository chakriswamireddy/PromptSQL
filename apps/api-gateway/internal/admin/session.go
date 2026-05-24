package admin

import (
	"context"

	internalauth "github.com/governance-platform/api-gateway/internal/auth"
	"github.com/google/uuid"
)

// SessionContext is a local alias so handlers don't import two different auth packages.
// It wraps pkg/auth.SessionContext and adds UUID-typed accessors for DB queries.
type SessionContext struct {
	// Parsed UUIDs (fail-fast: invalid UUIDs from JWT are rejected by middleware).
	TenantID  uuid.UUID
	UserID    uuid.UUID
	SessionID uuid.UUID
	RequestID string
}

// SessionFromContext retrieves the SessionContext from the request context.
// Returns nil if the middleware has not run or auth failed.
func SessionFromContext(ctx context.Context) *SessionContext {
	sc := internalauth.FromContext(ctx)
	if sc == nil {
		return nil
	}
	tenantID, err := uuid.Parse(sc.TenantID)
	if err != nil {
		return nil
	}
	userID, err := uuid.Parse(sc.UserID)
	if err != nil {
		return nil
	}
	sessionID, _ := uuid.Parse(sc.SessionID)

	return &SessionContext{
		inner:     sc,
		TenantID:  tenantID,
		UserID:    userID,
		SessionID: sessionID,
		RequestID: sc.RequestID,
	}
}
