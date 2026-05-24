// Package testdb provides helpers for integration tests that need a live PostgreSQL
// connection.  Tests import this package and call Open() to get a *sql.DB
// already connected to the test database.  The database URL is read from the
// TEST_DATABASE_URL environment variable; if absent, the test is skipped.
package testdb

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// Open returns a connected *sql.DB for integration tests.
// The test is skipped (not failed) if TEST_DATABASE_URL is unset, so unit
// test runs without a live DB pass cleanly.
func Open(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("testdb.Open: sql.Open: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("testdb.Open: ping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// WithTenantTx runs fn inside a transaction with app.tenant_id set to tenantID.
// The transaction is rolled back after fn returns so tests are isolated.
func WithTenantTx(t *testing.T, db *sql.DB, tenantID string, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("WithTenantTx: begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		"SELECT set_config('app.tenant_id', $1, true)", tenantID,
	); err != nil {
		t.Fatalf("WithTenantTx: set_config: %v", err)
	}

	fn(tx)
}

// MustExec executes a query and fails the test on error.
func MustExec(t *testing.T, tx *sql.Tx, query string, args ...any) sql.Result {
	t.Helper()
	res, err := tx.Exec(query, args...)
	if err != nil {
		t.Fatalf("MustExec(%q): %v", query, err)
	}
	return res
}

// CountRows returns the number of rows in table visible to the current session context.
func CountRows(t *testing.T, tx *sql.Tx, table string) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("CountRows(%s): %v", table, err)
	}
	return n
}
