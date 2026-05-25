//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace/noop"
)

// requireEnv skips the test if the named env var is empty.
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("skipping: %s not set", key)
	}
	return v
}

// newTestPool returns a pgxpool connected to TEST_DATABASE_URL.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := requireEnv(t, "TEST_DATABASE_URL")
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSyncer_ShouldSkip verifies the idempotency check returns true when
// the stored sync_version matches the data source's current policy version.
func TestSyncer_ShouldSkip(t *testing.T) {
	pool := newTestPool(t)
	log := zerolog.Nop()
	tracer := noop.NewTracerProvider().Tracer("")
	cfg := loadConfig()
	m := newMetrics()
	s := newSyncer(pool, nil, tracer, log, cfg, m, nil)

	ctx := context.Background()

	// Insert a fake tenant and engine_sync_state row.
	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('test-tenant-syncer', 'test-tenant-syncer')
		 ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	// Insert a dummy data source.
	var dsID string
	err = pool.QueryRow(ctx, `
		INSERT INTO data_sources (tenant_id, name, engine, secret_ref)
		VALUES ($1::uuid, 'test-ds-syncer', 'mysql', 'vault/test/mysql')
		ON CONFLICT DO NOTHING
		RETURNING id::text`,
		tenantID,
	).Scan(&dsID)
	if err != nil {
		// May fail if data_sources doesn't have all required fields; skip gracefully.
		t.Skipf("data_source insert failed (schema mismatch): %v", err)
	}

	const version = "v-abc-123"

	// Upsert engine_sync_state with this version.
	_, err = pool.Exec(ctx, `
		INSERT INTO engine_sync_state
		  (tenant_id, data_source_id, engine, sync_kind, sync_version, last_synced_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 'mysql', 'native_policy', $3, NOW(), NOW())
		ON CONFLICT (tenant_id, data_source_id, engine, sync_kind)
		DO UPDATE SET sync_version = EXCLUDED.sync_version, updated_at = NOW()
	`, tenantID, dsID, version)
	if err != nil {
		t.Fatalf("upsert sync state: %v", err)
	}

	ds := dataSourceRecord{
		ID:               dsID,
		Engine:           "mysql",
		TenantID:         tenantID,
		PolicySetVersion: version, // same version → should skip
	}

	skip, err := s.shouldSkip(ctx, ds)
	if err != nil {
		t.Fatalf("shouldSkip: %v", err)
	}
	if !skip {
		t.Error("expected shouldSkip=true when sync_version matches; got false")
	}

	// Change the version → should not skip.
	ds.PolicySetVersion = "v-xyz-999"
	skip, err = s.shouldSkip(ctx, ds)
	if err != nil {
		t.Fatalf("shouldSkip (changed): %v", err)
	}
	if skip {
		t.Error("expected shouldSkip=false when sync_version differs; got true")
	}
}

// TestSyncer_WriteEnforcementLog verifies rows land in native_enforcement_log
// with the correct status and partition routing.
func TestSyncer_WriteEnforcementLog(t *testing.T) {
	pool := newTestPool(t)
	log := zerolog.Nop()
	tracer := noop.NewTracerProvider().Tracer("")
	cfg := loadConfig()
	m := newMetrics()
	s := newSyncer(pool, nil, tracer, log, cfg, m, nil)

	ctx := context.Background()

	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('test-tenant-log', 'test-tenant-log')
		 ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	ds := dataSourceRecord{
		ID:               "00000000-0000-0000-0000-000000000001",
		Engine:           "postgres",
		TenantID:         tenantID,
		PolicySetVersion: "v-log-test",
	}

	err = s.writeEnforcementLog(ctx, ds, "apply", "ok", map[string]any{
		"policies_total": 3,
		"policies_ok":    3,
		"duration_ms":    42,
	})
	if err != nil {
		t.Fatalf("writeEnforcementLog: %v", err)
	}

	var count int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM native_enforcement_log
		WHERE tenant_id = $1::uuid
		  AND engine = 'postgres'
		  AND operation = 'apply'
		  AND status = 'ok'
		  AND created_at >= NOW() - INTERVAL '1 minute'
	`, tenantID).Scan(&count)
	if err != nil {
		t.Fatalf("count log rows: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 log row; got %d", count)
	}
}

// TestSyncer_UpdateSyncState verifies upsert semantics on engine_sync_state.
func TestSyncer_UpdateSyncState(t *testing.T) {
	pool := newTestPool(t)
	log := zerolog.Nop()
	tracer := noop.NewTracerProvider().Tracer("")
	cfg := loadConfig()
	m := newMetrics()
	s := newSyncer(pool, nil, tracer, log, cfg, m, nil)

	ctx := context.Background()

	var tenantID string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, slug) VALUES ('test-tenant-state', 'test-tenant-state')
		 ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id::text`,
	).Scan(&tenantID)
	if err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	var dsID string
	err = pool.QueryRow(ctx, `
		INSERT INTO data_sources (tenant_id, name, engine, secret_ref)
		VALUES ($1::uuid, 'test-ds-state', 'postgres', 'vault/test/pg')
		ON CONFLICT DO NOTHING
		RETURNING id::text`,
		tenantID,
	).Scan(&dsID)
	if err != nil {
		t.Skipf("data_source insert failed: %v", err)
	}

	ds := dataSourceRecord{
		ID:               dsID,
		Engine:           "postgres",
		TenantID:         tenantID,
		PolicySetVersion: "v-state-1",
	}

	// First upsert.
	if err := s.updateSyncState(ctx, ds, "v-state-1", nil); err != nil {
		t.Fatalf("updateSyncState (first): %v", err)
	}

	var storedVersion string
	var lastSynced time.Time
	err = pool.QueryRow(ctx, `
		SELECT sync_version, last_synced_at FROM engine_sync_state
		WHERE tenant_id = $1::uuid AND data_source_id = $2::uuid
		  AND engine = 'postgres' AND sync_kind = 'native_policy'
	`, tenantID, dsID).Scan(&storedVersion, &lastSynced)
	if err != nil {
		t.Fatalf("read sync state: %v", err)
	}
	if storedVersion != "v-state-1" {
		t.Errorf("expected sync_version=v-state-1; got %s", storedVersion)
	}

	// Second upsert with updated version.
	ds.PolicySetVersion = "v-state-2"
	if err := s.updateSyncState(ctx, ds, "v-state-2", nil); err != nil {
		t.Fatalf("updateSyncState (second): %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT sync_version FROM engine_sync_state
		WHERE tenant_id = $1::uuid AND data_source_id = $2::uuid
		  AND engine = 'postgres' AND sync_kind = 'native_policy'
	`, tenantID, dsID).Scan(&storedVersion); err != nil {
		t.Fatalf("read sync state (second): %v", err)
	}
	if storedVersion != "v-state-2" {
		t.Errorf("expected sync_version=v-state-2 after upsert; got %s", storedVersion)
	}
}
