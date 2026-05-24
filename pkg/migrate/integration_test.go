//go:build integration

package migrate_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/governance-platform/pkg/testdb"
)

// tenantA and tenantB match the pre-seeded fixture IDs in scripts/src/seed.ts
const (
	tenantA = "018f4e1a-0001-7000-8000-000000000001"
	tenantB = "018f4e1a-0002-7000-8000-000000000002"
)

// ── RLS isolation tests ───────────────────────────────────────────────────────

// TestRLS_CrossTenantSelectReturnsZeroRows proves tenant isolation: a query
// under tenant A cannot see rows that belong to tenant B.
func TestRLS_CrossTenantSelectReturnsZeroRows(t *testing.T) {
	db := testdb.Open(t)

	// Insert a marker role under tenant B
	testdb.WithTenantTx(t, db, tenantB, func(tx *sql.Tx) {
		testdb.MustExec(t, tx,
			`INSERT INTO roles (tenant_id, name, description, is_system)
			 VALUES ($1, 'cross-tenant-marker', 'should not bleed', false)
			 ON CONFLICT DO NOTHING`,
			tenantB,
		)
	})

	// Query as tenant A — the marker must not appear
	testdb.WithTenantTx(t, db, tenantA, func(tx *sql.Tx) {
		var count int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM roles WHERE name = 'cross-tenant-marker'`,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("cross-tenant RLS leak: tenantA sees tenantB row (count=%d)", count)
		}
	})
}

// TestRLS_MissingTenantContextReturnsZeroRows proves fail-closed behaviour:
// if app.tenant_id is absent, all tenant-scoped queries return empty results.
func TestRLS_MissingTenantContextReturnsZeroRows(t *testing.T) {
	db := testdb.Open(t)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Deliberately omit SET app.tenant_id
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows with no tenant context, got %d", count)
	}
}

// TestRLS_WrongTenantContextReturnsZeroRows verifies that a well-formed but
// incorrect tenant_id GUC returns zero rows (not a cross-tenant leak).
func TestRLS_WrongTenantContextReturnsZeroRows(t *testing.T) {
	db := testdb.Open(t)

	const nonExistentTenant = "00000000-0000-0000-0000-000000000099"
	testdb.WithTenantTx(t, db, nonExistentTenant, func(tx *sql.Tx) {
		n := testdb.CountRows(t, tx, "users")
		if n != 0 {
			t.Errorf("expected 0 rows for non-existent tenant, got %d", n)
		}
	})
}

// ── Hash-chain tests ──────────────────────────────────────────────────────────

// TestHashChain_AppendProducesNonNullHash inserts a policy_audit row and
// asserts both prev_hash and row_hash are populated by the trigger.
func TestHashChain_AppendProducesNonNullHash(t *testing.T) {
	db := testdb.Open(t)

	testdb.WithTenantTx(t, db, tenantA, func(tx *sql.Tx) {
		testdb.MustExec(t, tx,
			`INSERT INTO policy_audit
			   (tenant_id, actor_id, actor_token, action, outcome, created_at)
			 VALUES ($1, $2, $3, 'policy.create', 'success', $4)`,
			tenantA,
			"018f4e1b-0001-7000-8000-000000000001",
			fmt.Sprintf("tok_%s", tenantA),
			time.Now().UTC(),
		)

		var prevHash, rowHash []byte
		if err := tx.QueryRow(
			`SELECT prev_hash, row_hash FROM policy_audit
			  WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
			tenantA,
		).Scan(&prevHash, &rowHash); err != nil {
			t.Fatalf("scan hash fields: %v", err)
		}
		if len(prevHash) == 0 {
			t.Error("prev_hash is null/empty after first insert")
		}
		if len(rowHash) != 32 {
			t.Errorf("expected 32-byte SHA-256 row_hash, got %d bytes", len(rowHash))
		}
	})
}

// TestHashChain_GenesisRowUsesZeroSentinel verifies the first row in a tenant's
// chain uses the zero-byte sentinel as prev_hash.
func TestHashChain_GenesisRowUsesZeroSentinel(t *testing.T) {
	db := testdb.Open(t)

	// Use tenantB which may have no audit rows yet
	testdb.WithTenantTx(t, db, tenantB, func(tx *sql.Tx) {
		testdb.MustExec(t, tx,
			`INSERT INTO policy_audit
			   (tenant_id, actor_id, actor_token, action, outcome, created_at)
			 VALUES ($1, $2, 'genesis-tok', 'policy.create', 'success', $3)`,
			tenantB,
			"018f4e1b-0003-7000-8000-000000000003",
			time.Now().UTC(),
		)

		var prevHash []byte
		if err := tx.QueryRow(
			`SELECT prev_hash FROM policy_audit
			  WHERE tenant_id = $1 ORDER BY created_at ASC, id ASC LIMIT 1`,
			tenantB,
		).Scan(&prevHash); err != nil {
			t.Fatalf("scan genesis prev_hash: %v", err)
		}
		// Genesis prev_hash must be the zero sentinel '\x00'
		if len(prevHash) != 1 || prevHash[0] != 0x00 {
			t.Errorf("genesis prev_hash = %x; want 0x00", prevHash)
		}
	})
}

// TestHashChain_VerifyFunctionReturnsNilForIntactChain calls the DB-side
// verify_policy_audit_chain() function and asserts it returns NULL (intact).
func TestHashChain_VerifyFunctionReturnsNilForIntactChain(t *testing.T) {
	db := testdb.Open(t)

	testdb.WithTenantTx(t, db, tenantA, func(tx *sql.Tx) {
		// Append three sequential rows
		for i := 0; i < 3; i++ {
			testdb.MustExec(t, tx,
				`INSERT INTO policy_audit
				   (tenant_id, actor_id, actor_token, action, outcome, created_at)
				 VALUES ($1, $2, 'tok_verify', 'policy.update', 'success', $3)`,
				tenantA,
				"018f4e1b-0001-7000-8000-000000000001",
				time.Now().UTC(),
			)
		}

		var divergentID *string
		if err := tx.QueryRow(
			`SELECT verify_policy_audit_chain($1, '-infinity'::timestamptz)`,
			tenantA,
		).Scan(&divergentID); err != nil {
			t.Fatalf("verify_policy_audit_chain: %v", err)
		}
		if divergentID != nil {
			t.Errorf("intact chain reported divergence at row %s", *divergentID)
		}
	})
}

// TestHashChain_ConcurrentAppendsMaintainChain spawns 20 goroutines each
// appending one row; afterwards the chain must still verify cleanly.
func TestHashChain_ConcurrentAppendsMaintainChain(t *testing.T) {
	db := testdb.Open(t)

	const workers = 20
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		go func() {
			tx, err := db.Begin()
			if err != nil {
				errCh <- err
				return
			}
			defer tx.Rollback()

			if _, err := tx.Exec(
				`SELECT set_config('app.tenant_id', $1, true)`, tenantA,
			); err != nil {
				errCh <- err
				return
			}

			if _, err := tx.Exec(
				`INSERT INTO policy_audit
				   (tenant_id, actor_id, actor_token, action, outcome, created_at)
				 VALUES ($1, $2, 'tok_concurrent', 'policy.list', 'success', $3)`,
				tenantA,
				"018f4e1b-0001-7000-8000-000000000001",
				time.Now().UTC(),
			); err != nil {
				errCh <- err
				return
			}

			errCh <- tx.Commit()
		}()
	}

	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("worker error: %v", err)
		}
	}

	// Verify chain intact after concurrent writes
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantA); err != nil {
		t.Fatal(err)
	}

	var divergentID *string
	if err := tx.QueryRow(
		`SELECT verify_policy_audit_chain($1, '-infinity'::timestamptz)`, tenantA,
	).Scan(&divergentID); err != nil {
		t.Fatalf("verify after concurrent writes: %v", err)
	}
	if divergentID != nil {
		t.Errorf("chain diverged after concurrent appends at row %s", *divergentID)
	}
}
