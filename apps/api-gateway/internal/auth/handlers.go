package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	pkgauth "github.com/governance-platform/pkg/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const (
	maxFailedLogins = 5
	lockoutBase     = 15 * time.Minute
)

// Handler holds all auth endpoint logic.
type Handler struct {
	pool     *pgxpool.Pool
	jwt      *pkgauth.JWTService
	refresh  *RefreshStore
	mfa      *MFAService
	roles    *RoleResolver
	featureOn bool
}

// NewHandler returns a Handler.
func NewHandler(pool *pgxpool.Pool, jwt *pkgauth.JWTService, refresh *RefreshStore, mfa *MFAService, roles *RoleResolver, featureOn bool) *Handler {
	return &Handler{pool: pool, jwt: jwt, refresh: refresh, mfa: mfa, roles: roles, featureOn: featureOn}
}

// ─── Login ────────────────────────────────────────────────────────────────────

type loginRequest struct {
	TenantSlug string `json:"tenantSlug"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	TOTPCode   string `json:"totpCode,omitempty"`
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"` // seconds
	TokenType    string `json:"tokenType"`
}

// Login handles POST /v1/auth/login.
// It validates credentials, enforces lockout, checks MFA, mints tokens.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.Login")
	defer span.End()

	if !h.featureOn {
		writeError(w, http.StatusNotFound, "feature_disabled", "Authentication not enabled", "")
		return
	}

	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON body", "")
		return
	}
	if req.TenantSlug == "" || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "tenantSlug, email, and password are required", "")
		return
	}

	requestID := uuid.New().String()
	span.SetAttributes(attribute.String("auth.method", "password"))

	// Look up tenant.
	var tenantID, tenantStatus string
	err := h.pool.QueryRow(ctx,
		"SELECT id, status FROM tenants WHERE slug = $1", req.TenantSlug,
	).Scan(&tenantID, &tenantStatus)
	if err != nil {
		span.SetStatus(codes.Error, "tenant not found")
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid credentials", requestID)
		return
	}
	if tenantStatus != "active" {
		writeError(w, http.StatusForbidden, "tenant_suspended", "Tenant is suspended", requestID)
		return
	}

	// Look up user. Email+tenant uniqueness is enforced by DB constraint.
	var userID, passwordHash string
	var failedAttempts int
	var lockedUntil *time.Time
	var sessionInvalidatedAt *time.Time

	err = h.pool.QueryRow(ctx,
		`SELECT id, COALESCE(password_hash,''), failed_login_attempts, locked_until, session_invalidated_at
		 FROM users
		 WHERE email = $1 AND tenant_id = $2 AND status = 'active' AND deleted_at IS NULL`,
		req.Email, tenantID,
	).Scan(&userID, &passwordHash, &failedAttempts, &lockedUntil, &sessionInvalidatedAt)
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		span.SetStatus(codes.Error, "user not found")
		// Constant-time response to prevent user enumeration.
		_, _ = VerifyPassword("placeholder", "$argon2id$v=19$m=65536,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid credentials", requestID)
		return
	}

	// Check account lockout.
	if lockedUntil != nil && time.Now().Before(*lockedUntil) {
		writeError(w, http.StatusTooManyRequests, "account_locked",
			"Account temporarily locked. Try again later.", requestID)
		return
	}

	// Verify password.
	ok, err := VerifyPassword(req.Password, passwordHash)
	if err != nil || !ok {
		_ = h.incrementFailedLogins(ctx, userID, tenantID, failedAttempts)
		span.SetStatus(codes.Error, "invalid password")
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid credentials", requestID)
		return
	}

	// MFA check.
	enrolled, _ := h.mfa.IsEnrolled(ctx, userID)
	amr := []string{"pwd"}
	var mfaAt int64
	if enrolled {
		if req.TOTPCode == "" {
			writeError(w, http.StatusUnauthorized, "mfa_required", "TOTP code required", requestID)
			return
		}
		if err := h.mfa.Verify(ctx, userID, req.TOTPCode); err != nil {
			span.SetStatus(codes.Error, "invalid totp")
			writeError(w, http.StatusUnauthorized, "invalid_mfa", "Invalid TOTP code", requestID)
			return
		}
		amr = append(amr, "totp")
		mfaAt = time.Now().Unix()
		span.SetAttributes(attribute.Bool("auth.mfa", true))
	}

	// Reset failed login counter on success.
	_, _ = h.pool.Exec(ctx,
		"UPDATE users SET failed_login_attempts = 0, locked_until = NULL WHERE id = $1", userID)

	sessionID := uuid.New().String()

	// Mint tokens.
	accessToken, err := h.jwt.Sign(userID, tenantID, sessionID, amr, mfaAt)
	if err != nil {
		span.SetStatus(codes.Error, "token sign failed")
		writeError(w, http.StatusInternalServerError, "token_error", "Could not issue token", requestID)
		return
	}
	refreshToken, _, err := h.refresh.Issue(ctx, userID, tenantID, sessionID)
	if err != nil {
		span.SetStatus(codes.Error, "refresh token issue failed")
		writeError(w, http.StatusInternalServerError, "token_error", "Could not issue refresh token", requestID)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int((10 * time.Minute).Seconds()),
		TokenType:    "Bearer",
	})
}

// ─── Refresh ──────────────────────────────────────────────────────────────────

// Refresh handles POST /v1/auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.Refresh")
	defer span.End()

	requestID := uuid.New().String()
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &req); err != nil || req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "refreshToken required", requestID)
		return
	}

	old, err := h.refresh.Lookup(ctx, req.RefreshToken)
	if errors.Is(err, ErrTokenNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid_token", "Token not found", requestID)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "Token lookup failed", requestID)
		return
	}

	newCleartext, _, stolen, err := h.refresh.Rotate(ctx, req.RefreshToken, old.UserID, old.TenantID, old.SessionID)
	if stolen || errors.Is(err, ErrTokenReuse) {
		// Token theft detected — invalidate the whole user session.
		_, _ = h.pool.Exec(ctx,
			"UPDATE users SET session_invalidated_at = now() WHERE id = $1", old.UserID)
		span.SetStatus(codes.Error, "token reuse")
		writeError(w, http.StatusUnauthorized, "token_reuse", "Token reuse detected — all sessions revoked", requestID)
		return
	}
	if errors.Is(err, ErrTokenExpired) {
		writeError(w, http.StatusUnauthorized, "token_expired", "Refresh token expired", requestID)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "Could not rotate token", requestID)
		return
	}

	newAccess, err := h.jwt.Sign(old.UserID, old.TenantID, old.SessionID, nil, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "Could not issue token", requestID)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  newAccess,
		RefreshToken: newCleartext,
		ExpiresIn:    int((10 * time.Minute).Seconds()),
		TokenType:    "Bearer",
	})
}

// ─── Logout ───────────────────────────────────────────────────────────────────

// Logout handles POST /v1/auth/logout. Requires valid JWT via middleware.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.Logout")
	defer span.End()

	sc := MustFromContext(ctx)
	_ = h.refresh.RevokeSession(ctx, sc.UserID, sc.TenantID, sc.SessionID)
	span.SetAttributes(attribute.String("enduser.tenant", sc.TenantID))
	w.WriteHeader(http.StatusNoContent)
}

// LogoutEverywhere handles POST /v1/auth/logout-everywhere. Requires valid JWT.
func (h *Handler) LogoutEverywhere(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.LogoutEverywhere")
	defer span.End()

	sc := MustFromContext(ctx)
	_ = h.refresh.RevokeAllForUser(ctx, sc.UserID, sc.TenantID)
	_, _ = h.pool.Exec(ctx,
		"UPDATE users SET session_invalidated_at = now() WHERE id = $1 AND tenant_id = $2",
		sc.UserID, sc.TenantID)

	span.SetAttributes(attribute.String("enduser.tenant", sc.TenantID))
	w.WriteHeader(http.StatusNoContent)
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

// Sessions handles GET /v1/auth/sessions. Returns active session list.
func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.Sessions")
	defer span.End()

	sc := MustFromContext(ctx)
	sessions, err := h.refresh.ActiveSessions(ctx, sc.UserID, sc.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Could not list sessions", sc.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// DeleteSession handles DELETE /v1/auth/sessions/{id}.
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.DeleteSession")
	defer span.End()

	sc := MustFromContext(ctx)
	sessionID := r.PathValue("id")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "session id required", sc.RequestID)
		return
	}
	_ = h.refresh.RevokeSession(ctx, sc.UserID, sc.TenantID, sessionID)
	w.WriteHeader(http.StatusNoContent)
	span.SetAttributes(attribute.String("auth.revoked_session", sessionID))
}

// ─── MFA ──────────────────────────────────────────────────────────────────────

// MFAEnroll handles POST /v1/auth/mfa/enroll.
func (h *Handler) MFAEnroll(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.MFAEnroll")
	defer span.End()

	sc := MustFromContext(ctx)

	// Fetch email for TOTP issuer label.
	var email string
	_ = h.pool.QueryRow(ctx,
		"SELECT email FROM users WHERE id = $1", sc.UserID,
	).Scan(&email)

	result, err := h.mfa.StartEnroll(ctx, sc.UserID, sc.TenantID, email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mfa_error", "Could not start MFA enrollment", sc.RequestID)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// MFAVerify handles POST /v1/auth/mfa/verify.
func (h *Handler) MFAVerify(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.MFAVerify")
	defer span.End()

	sc := MustFromContext(ctx)
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "code required", sc.RequestID)
		return
	}
	if err := h.mfa.ConfirmEnroll(ctx, sc.UserID, req.Code); err != nil {
		if errors.Is(err, ErrInvalidTOTP) {
			writeError(w, http.StatusUnauthorized, "invalid_code", "Invalid TOTP code", sc.RequestID)
			return
		}
		writeError(w, http.StatusInternalServerError, "mfa_error", "MFA verification failed", sc.RequestID)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// MFADisable handles POST /v1/auth/mfa/disable.
func (h *Handler) MFADisable(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "auth.MFADisable")
	defer span.End()

	sc := MustFromContext(ctx)
	var req struct {
		Password string `json:"password"`
		Code     string `json:"totpCode"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "password and totpCode required", sc.RequestID)
		return
	}

	// Re-verify password before disabling MFA.
	var pwHash string
	_ = h.pool.QueryRow(ctx, "SELECT COALESCE(password_hash,'') FROM users WHERE id = $1", sc.UserID).Scan(&pwHash)
	if ok, _ := VerifyPassword(req.Password, pwHash); !ok {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid password", sc.RequestID)
		return
	}
	if err := h.mfa.Verify(ctx, sc.UserID, req.Code); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_code", "Invalid TOTP code", sc.RequestID)
		return
	}
	if err := h.mfa.Disable(ctx, sc.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "mfa_error", "Could not disable MFA", sc.RequestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func (h *Handler) incrementFailedLogins(ctx context.Context, userID, _ string, current int) error {
	next := current + 1
	var lockedUntil *time.Time
	if next >= maxFailedLogins {
		// Exponential lockout: 15 min × 2^(n-5) for n ≥ 5.
		exp := next - maxFailedLogins
		if exp > 5 {
			exp = 5
		}
		t := time.Now().Add(lockoutBase * (1 << exp))
		lockedUntil = &t
	}
	_, err := h.pool.Exec(ctx,
		"UPDATE users SET failed_login_attempts = $1, locked_until = $2 WHERE id = $3",
		next, lockedUntil, userID)
	return err
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	return json.NewDecoder(r.Body).Decode(v)
}
