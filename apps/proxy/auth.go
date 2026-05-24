package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const tokenRedisPrefix = "proxy:token:"
const tokenTTL = 15 * time.Minute

// connSession holds the SessionContext resolved for a proxy connection.
type connSession struct {
	tenantID     string
	userID       string
	sessionID    string
	dataSourceID string // resolved from DB, may be empty = any
	roles        []string
	traceID      string
	requestID    string
	isBreakGlass bool
}

// tokenPayload is stored in Redis by the API gateway token issuer.
type tokenPayload struct {
	TenantID     string    `json:"tenant_id"`
	UserID       string    `json:"user_id"`
	SessionID    string    `json:"session_id"`
	DataSourceID string    `json:"data_source_id,omitempty"`
	Roles        []string  `json:"roles"`
	IsBreakGlass bool      `json:"is_break_glass"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// tokenAuthenticator validates connection tokens from Redis.
type tokenAuthenticator struct {
	rdb *redis.Client
}

func newTokenAuthenticator(rdb *redis.Client) *tokenAuthenticator {
	return &tokenAuthenticator{rdb: rdb}
}

// Validate looks up the token in Redis and returns a session or an error.
// The raw token is never logged; only its sha256 hash is used for lookup.
func (a *tokenAuthenticator) Validate(ctx context.Context, token string) (*connSession, error) {
	if token == "" {
		proxyTokenValidations.WithLabelValues("empty").Inc()
		return nil, fmt.Errorf("empty token")
	}

	key := tokenRedisKey(token)
	data, err := a.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		proxyTokenValidations.WithLabelValues("not_found").Inc()
		return nil, fmt.Errorf("token not found or expired")
	}
	if err != nil {
		proxyTokenValidations.WithLabelValues("redis_error").Inc()
		return nil, fmt.Errorf("token lookup: %w", err)
	}

	var payload tokenPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		proxyTokenValidations.WithLabelValues("parse_error").Inc()
		return nil, fmt.Errorf("token payload parse: %w", err)
	}

	if time.Now().After(payload.ExpiresAt) {
		proxyTokenValidations.WithLabelValues("expired").Inc()
		return nil, fmt.Errorf("token expired")
	}

	proxyTokenValidations.WithLabelValues("ok").Inc()
	return &connSession{
		tenantID:     payload.TenantID,
		userID:       payload.UserID,
		sessionID:    payload.SessionID,
		dataSourceID: payload.DataSourceID,
		roles:        payload.Roles,
		isBreakGlass: payload.IsBreakGlass,
	}, nil
}

// tokenRedisKey returns the Redis key for a token (by hash — raw token never stored as key).
func tokenRedisKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%s%x", tokenRedisPrefix, h)
}

// StoreToken stores a token payload in Redis. Called by the api-gateway db-token handler.
func StoreToken(ctx context.Context, rdb *redis.Client, token string, payload tokenPayload) error {
	key := tokenRedisKey(token)
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal token payload: %w", err)
	}
	ttl := time.Until(payload.ExpiresAt)
	if ttl <= 0 {
		ttl = tokenTTL
	}
	return rdb.Set(ctx, key, data, ttl).Err()
}
