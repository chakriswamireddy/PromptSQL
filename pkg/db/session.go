// Package db enforces the SET LOCAL session discipline required by the platform's
// Row-Level Security policies. All database access MUST go through WithSession;
// direct pool.Query / pool.Exec calls are rejected by the semgrep rule at
// .semgrep/db-discipline.yaml.
package db

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionData is the minimal interface that auth.SessionContext satisfies.
// Keeping this interface here avoids a circular import between pkg/db and pkg/auth.
type SessionData interface {
	GetUserID() string
	GetTenantID() string
	GetSessionID() string
	GetRequestID() string
	GetTraceID() string
	GetBreakGlass() bool
}

// Pool wraps pgxpool.Pool and provides the WithSession entry point.
type Pool struct {
	inner *pgxpool.Pool
}

// New wraps an existing pgxpool.
func New(inner *pgxpool.Pool) *Pool { return &Pool{inner: inner} }

// Inner exposes the underlying pool for libraries that require *pgxpool.Pool directly.
func (p *Pool) Inner() *pgxpool.Pool { return p.inner }

// WithSession runs fn inside a read-committed transaction with the full suite of
// app.* session GUCs set from sd and the role SET LOCAL to role (default: app_read).
// The transaction is automatically committed on nil return or rolled back on error.
//
// SET LOCAL is equivalent to set_config(..., true) with pgxpool transaction pooling —
// both are scoped to the transaction and cleared on commit/rollback. This satisfies
// PgBouncer transaction-mode pooling safety.
func (p *Pool) WithSession(ctx context.Context, sd SessionData, fn func(pgx.Tx) error, opts ...Option) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return p.inner.BeginTxFunc(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := applySessionGUCs(ctx, tx, sd, o.role); err != nil {
			return err
		}
		return fn(tx)
	})
}

// WithSessionSerializable is like WithSession but uses serializable isolation.
// Use only when you need to detect write-write conflicts (e.g., refresh-token rotation).
func (p *Pool) WithSessionSerializable(ctx context.Context, sd SessionData, fn func(pgx.Tx) error, opts ...Option) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	return p.inner.BeginTxFunc(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		if err := applySessionGUCs(ctx, tx, sd, o.role); err != nil {
			return err
		}
		return fn(tx)
	})
}

// applySessionGUCs issues SET LOCAL ROLE and set_config for all app.* parameters.
// Using set_config(key, value, true) is identical to SET LOCAL for GUCs and is
// safe with PgBouncer transaction pooling.
func applySessionGUCs(ctx context.Context, tx pgx.Tx, sd SessionData, role string) error {
	// Allowlist role names to prevent injection.
	switch role {
	case "app_read", "app_write", "app_admin":
	default:
		return fmt.Errorf("db: invalid role %q — must be app_read, app_write, or app_admin", role)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL ROLE %s", role)); err != nil {
		return fmt.Errorf("db: SET LOCAL ROLE %s: %w", role, err)
	}
	_, err := tx.Exec(ctx,
		"SELECT set_config('app.user_id',     $1, true),"+
			"       set_config('app.tenant_id',   $2, true),"+
			"       set_config('app.session_id',  $3, true),"+
			"       set_config('app.request_id',  $4, true),"+
			"       set_config('app.trace_id',    $5, true),"+
			"       set_config('app.break_glass', $6, true)",
		sd.GetUserID(),
		sd.GetTenantID(),
		sd.GetSessionID(),
		sd.GetRequestID(),
		sd.GetTraceID(),
		strconv.FormatBool(sd.GetBreakGlass()),
	)
	if err != nil {
		return fmt.Errorf("db: set_config GUCs: %w", err)
	}
	return nil
}

// Option configures WithSession behaviour.
type Option func(*options)

type options struct{ role string }

func defaultOptions() options { return options{role: "app_read"} }

// WithRole overrides the SET LOCAL ROLE (default: app_read).
// Allowed values: "app_read", "app_write", "app_admin".
func WithRole(role string) Option { return func(o *options) { o.role = role } }
