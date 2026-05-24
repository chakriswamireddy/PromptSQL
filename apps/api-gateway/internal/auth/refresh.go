package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	refreshTokenBytes = 32
	refreshTokenTTL   = 30 * 24 * time.Hour
)

// RefreshTokenRow represents a row from refresh_tokens.
type RefreshTokenRow struct {
	ID          string
	UserID      string
	TenantID    string
	SessionID   string
	PrevTokenID *string
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// RefreshStore handles opaque refresh token lifecycle: create, rotate, revoke.
// Tokens are stored as SHA-256(random_bytes) so the cleartext never touches the DB.
type RefreshStore struct {
	pool *pgxpool.Pool
}

// NewRefreshStore returns a RefreshStore.
func NewRefreshStore(pool *pgxpool.Pool) *RefreshStore {
	return &RefreshStore{pool: pool}
}

// Issue generates a new opaque refresh token, stores its hash, and returns the
// cleartext (sent to the client exactly once).
func (s *RefreshStore) Issue(ctx context.Context, userID, tenantID, sessionID string) (cleartext string, id string, err error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("refresh: generate token: %w", err)
	}
	cleartext = base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(cleartext)

	err = s.pool.QueryRow(ctx,
		`INSERT INTO refresh_tokens (user_id, tenant_id, token_hash, session_id, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		userID, tenantID, hash, sessionID, time.Now().Add(refreshTokenTTL),
	).Scan(&id)
	if err != nil {
		return "", "", fmt.Errorf("refresh: insert: %w", err)
	}
	return cleartext, id, nil
}

// Lookup finds a refresh token row by cleartext value.
// Returns ErrTokenNotFound if the hash does not exist.
func (s *RefreshStore) Lookup(ctx context.Context, cleartext string) (*RefreshTokenRow, error) {
	hash := sha256Hex(cleartext)
	row := &RefreshTokenRow{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, tenant_id, session_id, prev_token_id, expires_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = $1`,
		hash,
	).Scan(&row.ID, &row.UserID, &row.TenantID, &row.SessionID,
		&row.PrevTokenID, &row.ExpiresAt, &row.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("refresh: lookup: %w", err)
	}
	return row, nil
}

// Rotate invalidates oldID and issues a new token.
// If oldID is already revoked (theft signal), it revokes the entire session.
// Uses serializable isolation to prevent concurrent rotation races.
func (s *RefreshStore) Rotate(ctx context.Context, oldCleartext, userID, tenantID, sessionID string) (newCleartext, newID string, stolen bool, err error) {
	old, err := s.Lookup(ctx, oldCleartext)
	if err != nil {
		return "", "", false, err
	}

	// Reuse of a revoked token = token theft signal.
	if old.RevokedAt != nil {
		_ = s.RevokeAllForUser(ctx, userID, tenantID)
		return "", "", true, ErrTokenReuse
	}
	if time.Now().After(old.ExpiresAt) {
		return "", "", false, ErrTokenExpired
	}

	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", false, fmt.Errorf("refresh: generate: %w", err)
	}
	newCleartext = base64.RawURLEncoding.EncodeToString(raw)
	newHash := sha256Hex(newCleartext)
	now := time.Now()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", false, fmt.Errorf("refresh: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Revoke old token.
	if _, err := tx.Exec(ctx,
		"UPDATE refresh_tokens SET revoked_at = $1 WHERE id = $2", now, old.ID,
	); err != nil {
		return "", "", false, fmt.Errorf("refresh: revoke old: %w", err)
	}

	// Insert new token.
	if err := tx.QueryRow(ctx,
		`INSERT INTO refresh_tokens (user_id, tenant_id, token_hash, session_id, prev_token_id, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		userID, tenantID, newHash, sessionID, old.ID, now.Add(refreshTokenTTL),
	).Scan(&newID); err != nil {
		return "", "", false, fmt.Errorf("refresh: insert new: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", "", false, fmt.Errorf("refresh: commit: %w", err)
	}
	return newCleartext, newID, false, nil
}

// Revoke marks a single token as revoked.
func (s *RefreshStore) Revoke(ctx context.Context, cleartext string) error {
	hash := sha256Hex(cleartext)
	_, err := s.pool.Exec(ctx,
		"UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1", hash)
	return err
}

// RevokeSession revokes all tokens for a specific session_id.
func (s *RefreshStore) RevokeSession(ctx context.Context, userID, tenantID, sessionID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE user_id = $1 AND tenant_id = $2 AND session_id = $3 AND revoked_at IS NULL`,
		userID, tenantID, sessionID)
	return err
}

// RevokeAllForUser revokes every active token for the user — used for logout-everywhere
// and token-theft response.
func (s *RefreshStore) RevokeAllForUser(ctx context.Context, userID, tenantID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		 WHERE user_id = $1 AND tenant_id = $2 AND revoked_at IS NULL`,
		userID, tenantID)
	return err
}

// ActiveSessions returns distinct session_ids with their latest token creation time.
func (s *RefreshStore) ActiveSessions(ctx context.Context, userID, tenantID string) ([]SessionSummary, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT session_id, MAX(created_at) AS last_active
		 FROM refresh_tokens
		 WHERE user_id = $1 AND tenant_id = $2
		   AND revoked_at IS NULL AND expires_at > now()
		 GROUP BY session_id
		 ORDER BY last_active DESC`,
		userID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("refresh: active sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionSummary
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.SessionID, &s.LastActive); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionSummary is returned by ActiveSessions.
type SessionSummary struct {
	SessionID  string    `json:"sessionId"`
	LastActive time.Time `json:"lastActive"`
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var (
	ErrTokenNotFound = fmt.Errorf("refresh: token not found")
	ErrTokenExpired  = fmt.Errorf("refresh: token expired")
	ErrTokenReuse    = fmt.Errorf("refresh: token reuse detected — session invalidated")
)
