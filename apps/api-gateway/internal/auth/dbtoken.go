package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	dbTokenTTL      = 15 * time.Minute
	dbTokenRedisPfx = "proxy:token:"
)

// DBTokenRequest is the body for POST /v1/db-token.
type DBTokenRequest struct {
	DataSourceID string `json:"data_source_id,omitempty"`
}

// DBTokenResponse is returned to the client (BI tool, notebook, etc.).
type DBTokenResponse struct {
	Token        string    `json:"token"`
	ExpiresAt    time.Time `json:"expires_at"`
	DataSourceID string    `json:"data_source_id,omitempty"`
	ProxyHost    string    `json:"proxy_host"`
	ProxyPort    int       `json:"proxy_port"`
}

// dbTokenPayload is stored in Redis and consumed by the proxy on connection.
type dbTokenPayload struct {
	TenantID     string    `json:"tenant_id"`
	UserID       string    `json:"user_id"`
	SessionID    string    `json:"session_id"`
	DataSourceID string    `json:"data_source_id,omitempty"`
	Roles        []string  `json:"roles"`
	IsBreakGlass bool      `json:"is_break_glass"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// DBTokenHandler issues short-lived connection tokens for the PG proxy.
type DBTokenHandler struct {
	rdb       *redis.Client
	proxyHost string
	proxyPort int
}

// NewDBTokenHandler creates the handler.
// proxyHost and proxyPort are the proxy's external address advertised to clients.
func NewDBTokenHandler(rdb *redis.Client, proxyHost string, proxyPort int) *DBTokenHandler {
	return &DBTokenHandler{rdb: rdb, proxyHost: proxyHost, proxyPort: proxyPort}
}

// IssueToken handles POST /v1/db-token.
// Auth middleware must run before this handler — it reads SessionContext from context.
func (h *DBTokenHandler) IssueToken(w http.ResponseWriter, r *http.Request) {
	sc := FromContext(r.Context())
	if sc == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session required", "")
		return
	}

	var req DBTokenRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON", "")
			return
		}
	}

	rawToken, err := generateDBToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "token generation failed", "")
		return
	}

	expiresAt := time.Now().Add(dbTokenTTL)
	payload := dbTokenPayload{
		TenantID:     sc.TenantID,
		UserID:       sc.UserID,
		SessionID:    sc.SessionID,
		DataSourceID: req.DataSourceID,
		Roles:        sc.Roles,
		IsBreakGlass: sc.IsBreakGlass,
		ExpiresAt:    expiresAt,
	}

	if err := writeTokenToRedis(r.Context(), h.rdb, rawToken, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "token storage failed", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, DBTokenResponse{
		Token:        rawToken,
		ExpiresAt:    expiresAt,
		DataSourceID: req.DataSourceID,
		ProxyHost:    h.proxyHost,
		ProxyPort:    h.proxyPort,
	})
}

// RevokeToken handles DELETE /v1/db-token to invalidate an issued token.
func (h *DBTokenHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	sc := FromContext(r.Context())
	if sc == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session required", "")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "token required", "")
		return
	}

	if err := h.rdb.Del(r.Context(), dbTokenKey(req.Token)).Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "revocation failed", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func generateDBToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func writeTokenToRedis(ctx context.Context, rdb *redis.Client, token string, payload dbTokenPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ttl := time.Until(payload.ExpiresAt)
	if ttl <= 0 {
		ttl = dbTokenTTL
	}
	return rdb.Set(ctx, dbTokenKey(token), data, ttl).Err()
}

// dbTokenKey returns the Redis key for a given raw token.
// Key is sha256(token) so the raw token never appears in Redis keyspace or logs.
func dbTokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%s%x", dbTokenRedisPfx, h)
}
