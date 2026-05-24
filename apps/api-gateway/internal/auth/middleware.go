package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	pkgauth "github.com/governance-platform/pkg/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var tracer = otel.Tracer("api-gateway/auth")

// Dependencies groups the external clients the middleware needs.
type Dependencies struct {
	JWT      *pkgauth.JWTService
	JTI      *pkgauth.JTIStore
	HMAC     *pkgauth.HMACService
	Roles    *RoleResolver
	Pool     *pgxpool.Pool
	Redis    *redis.Client
}

// Middleware returns an HTTP middleware that:
//  1. Extracts and verifies the Bearer JWT (EdDSA only).
//  2. Checks JTI replay via Redis.
//  3. Validates session_invalidated_at against token iat.
//  4. Resolves roles/attributes from DB (60 s Redis cache).
//  5. Checks tenant suspension.
//  6. Builds SessionContext and attaches to request context.
//  7. Signs and attaches HMAC headers for downstream services.
func Middleware(deps Dependencies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), "auth.middleware")
			defer span.End()

			requestID := uuid.New().String()
			w.Header().Set("X-Request-ID", requestID)

			// 1. Extract Bearer token.
			tokenStr, ok := extractBearer(r)
			if !ok {
				span.SetStatus(codes.Error, "missing bearer")
				writeError(w, http.StatusUnauthorized, "missing_token", "Authorization header required", requestID)
				return
			}

			// 2. Verify signature + standard claims.
			claims, err := deps.JWT.Verify(tokenStr)
			if err != nil {
				span.SetStatus(codes.Error, "invalid token")
				span.SetAttributes(attribute.String("auth.error", err.Error()))
				writeError(w, http.StatusUnauthorized, "invalid_token", "Token verification failed", requestID)
				return
			}

			// 3. JTI replay check (fail-closed: if Redis is down, reject).
			if err := deps.JTI.Claim(ctx, claims.ID); err != nil {
				if errors.Is(err, pkgauth.ErrReplay) {
					span.SetStatus(codes.Error, "token replay")
					writeError(w, http.StatusUnauthorized, "token_replay", "Token has already been used", requestID)
					return
				}
				// Redis unavailable → fail closed.
				span.SetStatus(codes.Error, "jti store unavailable")
				writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "Auth service temporarily unavailable", requestID)
				return
			}

			userID := claims.Subject
			tenantID := claims.Tenant
			sessionID := claims.SessionID

			// 4. Check session_invalidated_at (logout-everywhere).
			if err := checkSessionValid(ctx, deps.Pool, userID, tenantID, claims.IssuedAt.Time); err != nil {
				span.SetStatus(codes.Error, "session invalidated")
				writeError(w, http.StatusUnauthorized, "session_invalidated", "Session has been revoked", requestID)
				return
			}

			// 5. Check tenant status.
			if err := checkTenantActive(ctx, deps.Pool, tenantID); err != nil {
				span.SetStatus(codes.Error, "tenant suspended")
				writeError(w, http.StatusForbidden, "tenant_suspended", "Tenant account is suspended", requestID)
				return
			}

			// 6. Resolve roles from DB/cache.
			roles, err := deps.Roles.Resolve(ctx, tenantID, userID)
			if err != nil {
				span.SetStatus(codes.Error, "role resolve failed")
				writeError(w, http.StatusServiceUnavailable, "role_resolve_failed", "Could not resolve roles", requestID)
				return
			}

			// 7. Build SessionContext.
			now := time.Now()
			var mfaAt *time.Time
			if claims.MFAAt > 0 {
				t := time.Unix(claims.MFAAt, 0)
				mfaAt = &t
			}
			sc := &pkgauth.SessionContext{
				UserID:      userID,
				TenantID:    tenantID,
				SessionID:   sessionID,
				SubjectKind: pkgauth.SubjectKindUser,
				Roles:       roles,
				Attributes: pkgauth.SessionAttributes{
					DeviceTrust:  pkgauth.DeviceTrustUnknown,
					NetworkTrust: pkgauth.NetworkTrustPublic,
				},
				RequestID: requestID,
				TraceID:   span.SpanContext().TraceID().String(),
				IssuedAt:  now,
				ExpiresAt: claims.ExpiresAt.Time,
				AMR:       claims.AMR,
				MFAAt:     mfaAt,
			}

			span.SetAttributes(
				attribute.String("enduser.tenant", tenantID),
				attribute.Bool("auth.mfa", len(claims.AMR) > 1),
				attribute.Bool("auth.cache_hit", false), // refined in Roles
			)

			// 8. Attach HMAC headers for downstream.
			if deps.HMAC != nil {
				if ctxB64, sigB64, keyID, err := deps.HMAC.Sign(sc); err == nil {
					r.Header.Set("X-Session-Context", ctxB64)
					r.Header.Set("X-Session-Context-Sig", sigB64)
					r.Header.Set("X-Session-Context-KeyId", keyID)
				}
			}

			ctx = WithContext(ctx, sc)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearer extracts the token from "Authorization: Bearer <token>".
func extractBearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok == "" {
		return "", false
	}
	return tok, true
}

// checkSessionValid returns an error if the token was issued before
// users.session_invalidated_at (meaning a logout-everywhere was performed).
func checkSessionValid(ctx context.Context, pool *pgxpool.Pool, userID, tenantID string, tokenIAT time.Time) error {
	var invalidatedAt *time.Time
	err := pool.QueryRow(ctx,
		"SELECT session_invalidated_at FROM users WHERE id = $1 AND tenant_id = $2",
		userID, tenantID,
	).Scan(&invalidatedAt)
	if err != nil {
		return err
	}
	if invalidatedAt != nil && tokenIAT.Before(*invalidatedAt) {
		return errors.New("session invalidated")
	}
	return nil
}

// checkTenantActive returns an error if the tenant is suspended or deleted.
func checkTenantActive(ctx context.Context, pool *pgxpool.Pool, tenantID string) error {
	var status string
	err := pool.QueryRow(ctx,
		"SELECT status FROM tenants WHERE id = $1", tenantID,
	).Scan(&status)
	if err != nil {
		return err
	}
	if status != "active" {
		return errors.New("tenant not active")
	}
	return nil
}
