// Package breakglass manages break-glass sessions: dual approval, time-boxing,
// auto-revocation, and the dedicated audit hash chain.
package breakglass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status values for a break-glass session.
const (
	StatusPendingApproval = "pending_approval"
	StatusActive          = "active"
	StatusTerminated      = "terminated"
	StatusExpired         = "expired"
)

// Session is a break-glass session record.
type Session struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	PrincipalID     string          `json:"principal_id"`
	InitiatorID     string          `json:"initiator_id"`
	Scope           json.RawMessage `json:"scope"`
	Reason          string          `json:"reason"`
	Approvers       []string        `json:"approvers"`
	Status          string          `json:"status"`
	MaxDurationSec  int             `json:"max_duration_sec"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
	TerminatedAt    *time.Time      `json:"terminated_at,omitempty"`
	TerminatedBy    *string         `json:"terminated_by,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

// ErrNotFound is returned when a session does not exist.
var ErrNotFound = errors.New("breakglass: session not found")

// ErrAlreadyApproved is returned when the approver has already approved.
var ErrAlreadyApproved = errors.New("breakglass: already approved by this user")

// ErrInitiatorCannotApprove is returned when the initiator tries to approve.
var ErrInitiatorCannotApprove = errors.New("breakglass: initiator cannot approve their own request")

// Store provides database access for break-glass sessions.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Create inserts a new pending break-glass session.
func (s *Store) Create(ctx context.Context, sess *Session) (string, error) {
	const q = `
		INSERT INTO breakglass_sessions
		    (tenant_id, principal_id, initiator_id, scope, reason, max_duration_sec)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, created_at`

	var id string
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, q,
		sess.TenantID, sess.PrincipalID, sess.InitiatorID,
		sess.Scope, sess.Reason, sess.MaxDurationSec,
	).Scan(&id, &createdAt)
	if err != nil {
		return "", fmt.Errorf("breakglass create: %w", err)
	}
	return id, nil
}

// Approve adds an approver to the session.
// Returns ErrInitiatorCannotApprove if the approver is the initiator.
// Returns ErrAlreadyApproved if the approver has already approved.
// When approverCount reaches 2, the session becomes active atomically.
func (s *Store) Approve(ctx context.Context, tenantID, sessionID, approverID string) (*Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("breakglass approve: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the row to prevent double-approval races.
	const lockQ = `
		SELECT id, tenant_id, initiator_id, approvers, status, max_duration_sec
		FROM breakglass_sessions
		WHERE id = $1 AND tenant_id = $2
		FOR UPDATE`
	var sess Session
	var approversJSON []byte
	err = tx.QueryRow(ctx, lockQ, sessionID, tenantID).Scan(
		&sess.ID, &sess.TenantID, &sess.InitiatorID, &approversJSON, &sess.Status, &sess.MaxDurationSec,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("breakglass approve: lock: %w", err)
	}

	if sess.InitiatorID == approverID {
		return nil, ErrInitiatorCannotApprove
	}
	if err := json.Unmarshal(approversJSON, &sess.Approvers); err != nil {
		sess.Approvers = nil
	}
	for _, a := range sess.Approvers {
		if a == approverID {
			return nil, ErrAlreadyApproved
		}
	}

	sess.Approvers = append(sess.Approvers, approverID)
	newApproversJSON, _ := json.Marshal(sess.Approvers)

	newStatus := sess.Status
	var startedAt, expiresAt *time.Time
	if len(sess.Approvers) >= 2 && sess.Status == StatusPendingApproval {
		newStatus = StatusActive
		now := time.Now().UTC()
		exp := now.Add(time.Duration(sess.MaxDurationSec) * time.Second)
		startedAt = &now
		expiresAt = &exp
	}

	const updateQ = `
		UPDATE breakglass_sessions
		SET approvers=$1, status=$2, started_at=$3, expires_at=$4
		WHERE id=$5
		RETURNING id, tenant_id, principal_id, initiator_id, scope, reason,
		          approvers, status, max_duration_sec, started_at, expires_at, created_at`

	row := tx.QueryRow(ctx, updateQ, newApproversJSON, newStatus, startedAt, expiresAt, sessionID)
	updated, err := scanSession(row)
	if err != nil {
		return nil, fmt.Errorf("breakglass approve: update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("breakglass approve: commit: %w", err)
	}
	return updated, nil
}

// Terminate ends a session early (by a super-admin or the initiator).
func (s *Store) Terminate(ctx context.Context, tenantID, sessionID, actorID string) (*Session, error) {
	const q = `
		UPDATE breakglass_sessions
		SET status=$1, terminated_at=now(), terminated_by=$2
		WHERE id=$3 AND tenant_id=$4 AND status IN ('pending_approval','active')
		RETURNING id, tenant_id, principal_id, initiator_id, scope, reason,
		          approvers, status, max_duration_sec, started_at, expires_at,
		          terminated_at, terminated_by, created_at`

	row := s.pool.QueryRow(ctx, q, StatusTerminated, actorID, sessionID, tenantID)
	sess, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// Get fetches a single session by ID.
func (s *Store) Get(ctx context.Context, tenantID, sessionID string) (*Session, error) {
	const q = `
		SELECT id, tenant_id, principal_id, initiator_id, scope, reason,
		       approvers, status, max_duration_sec, started_at, expires_at,
		       terminated_at, terminated_by, created_at
		FROM breakglass_sessions
		WHERE id=$1 AND tenant_id=$2`

	row := s.pool.QueryRow(ctx, q, sessionID, tenantID)
	sess, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
}

// ListActive returns all active sessions for a tenant.
func (s *Store) ListActive(ctx context.Context, tenantID string) ([]*Session, error) {
	const q = `
		SELECT id, tenant_id, principal_id, initiator_id, scope, reason,
		       approvers, status, max_duration_sec, started_at, expires_at,
		       terminated_at, terminated_by, created_at
		FROM breakglass_sessions
		WHERE tenant_id=$1 AND status IN ('pending_approval','active')
		ORDER BY created_at DESC`

	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("breakglass list: %w", err)
	}
	defer rows.Close()

	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ExpireStale marks all active sessions whose expires_at has passed as expired.
func (s *Store) ExpireStale(ctx context.Context) (int64, error) {
	const q = `
		UPDATE breakglass_sessions
		SET status='expired'
		WHERE status='active' AND expires_at < now()`

	tag, err := s.pool.Exec(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("breakglass expire: %w", err)
	}
	return tag.RowsAffected(), nil
}

// IsActive reports whether the given principal has an active break-glass session.
func (s *Store) IsActive(ctx context.Context, tenantID, principalID string) (bool, error) {
	const q = `
		SELECT 1 FROM breakglass_sessions
		WHERE tenant_id=$1 AND principal_id=$2 AND status='active' AND expires_at > now()
		LIMIT 1`

	var dummy int
	err := s.pool.QueryRow(ctx, q, tenantID, principalID).Scan(&dummy)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AppendAudit inserts a break-glass audit entry.
func (s *Store) AppendAudit(ctx context.Context, tenantID, sessionID, actorID, action string, meta map[string]any) error {
	metaJSON, _ := json.Marshal(meta)
	const q = `
		INSERT INTO breakglass_audit (tenant_id, session_id, actor_id, action, metadata)
		VALUES ($1,$2,$3,$4,$5)`
	_, err := s.pool.Exec(ctx, q, tenantID, sessionID, actorID, action, metaJSON)
	return err
}

// scanSession scans a breakglass_sessions row.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (*Session, error) {
	var sess Session
	var approversJSON []byte
	var scopeJSON []byte
	err := row.Scan(
		&sess.ID, &sess.TenantID, &sess.PrincipalID, &sess.InitiatorID,
		&scopeJSON, &sess.Reason, &approversJSON, &sess.Status,
		&sess.MaxDurationSec, &sess.StartedAt, &sess.ExpiresAt,
		&sess.TerminatedAt, &sess.TerminatedBy, &sess.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	sess.Scope = scopeJSON
	if len(approversJSON) > 0 {
		_ = json.Unmarshal(approversJSON, &sess.Approvers)
	}
	return &sess, nil
}
