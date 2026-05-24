package main

import (
	"testing"
)

// TestDenylistCheck verifies the side-channel denylist rejects banned patterns.
func TestDenylistCheck(t *testing.T) {
	cases := []struct {
		sql      string
		wantDeny bool
	}{
		// Allowed
		{"SELECT id, name FROM users WHERE tenant_id = $1", false},
		{"SELECT u.id FROM users u JOIN orders o ON u.id = o.user_id", false},
		// System catalog access
		{"SELECT * FROM pg_catalog.pg_class", true},
		{"SELECT * FROM information_schema.tables", true},
		{"SELECT * FROM pg_stat_activity", true},
		// Side-effecting functions
		{"SELECT pg_sleep(10)", true},
		{"SELECT pg_read_file('/etc/passwd')", true},
		// COPY
		{"COPY users FROM '/tmp/data.csv'", true},
		// DDL/DML
		{"INSERT INTO users VALUES (1, 'evil')", true},
		{"UPDATE users SET admin = true", true},
		{"DROP TABLE users", true},
		// SET ROLE from client
		{"SET ROLE superuser", true},
		{"SET LOCAL ROLE app_admin", true},
		{"SET app.tenant_id = '00000000'", true},
		// Multi-statement
		{"SELECT 1; DROP TABLE users", true},
	}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			reason := denylistCheck(tc.sql)
			denied := reason != ""
			if denied != tc.wantDeny {
				t.Errorf("denylistCheck(%q) = %q (denied=%v), want denied=%v",
					tc.sql, reason, denied, tc.wantDeny)
			}
		})
	}
}

// TestIsSelectStatement verifies SELECT detection.
func TestIsSelectStatement(t *testing.T) {
	cases := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", true},
		{"  SELECT id FROM users", true},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", true},
		{"INSERT INTO t VALUES (1)", false},
		{"UPDATE t SET x = 1", false},
		{"DELETE FROM t", false},
	}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			got := isSelectStatement(tc.sql)
			if got != tc.want {
				t.Errorf("isSelectStatement(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// TestExtractCandidateTables verifies table extraction from SQL.
func TestExtractCandidateTables(t *testing.T) {
	cases := []struct {
		sql    string
		tables []string
	}{
		{
			"SELECT * FROM users",
			[]string{"users"},
		},
		{
			"SELECT u.id FROM users u JOIN orders o ON u.id = o.user_id",
			[]string{"users", "orders"},
		},
		{
			"SELECT * FROM public.users",
			[]string{"users"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			got := extractCandidateTables(tc.sql)
			if len(got) != len(tc.tables) {
				t.Errorf("extractCandidateTables(%q) = %v, want %v", tc.sql, got, tc.tables)
				return
			}
			seen := make(map[string]bool)
			for _, g := range got {
				seen[g] = true
			}
			for _, want := range tc.tables {
				if !seen[want] {
					t.Errorf("extractCandidateTables(%q) missing %q, got %v", tc.sql, want, got)
				}
			}
		})
	}
}

// TestQueryHash verifies query hash is deterministic.
func TestQueryHash(t *testing.T) {
	h1 := queryHash("SELECT * FROM users", nil)
	h2 := queryHash("SELECT * FROM users", nil)
	if h1 != h2 {
		t.Errorf("queryHash is not deterministic: %q != %q", h1, h2)
	}

	h3 := queryHash("SELECT * FROM orders", nil)
	if h1 == h3 {
		t.Errorf("different SQL produced same hash")
	}
}

// TestNormalizeSQL verifies whitespace normalization.
func TestNormalizeSQL(t *testing.T) {
	got := normalizeSQL("  SELECT\n  id\n  FROM\n  users  ")
	want := "select id from users"
	if got != want {
		t.Errorf("normalizeSQL = %q, want %q", got, want)
	}
}
