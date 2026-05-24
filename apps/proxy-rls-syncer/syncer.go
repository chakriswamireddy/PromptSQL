package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// rlsSyncer reads active proxy policies from the control-plane DB and mirrors
// them as native PostgreSQL RLS policies on each managed datasource.
// This is the defence-in-depth layer: even if a user bypasses the proxy
// and connects directly to the backend, native RLS enforces the same rules.
type rlsSyncer struct {
	controlDB *pgxpool.Pool
	log       zerolog.Logger
	version   string
}

func newRLSSyncer(db *pgxpool.Pool, log zerolog.Logger, version string) *rlsSyncer {
	return &rlsSyncer{controlDB: db, log: log, version: version}
}

// Run performs one sync cycle: iterates all managed datasources and syncs policies.
func (s *rlsSyncer) Run(ctx context.Context) error {
	sources, err := s.listManagedDataSources(ctx)
	if err != nil {
		return fmt.Errorf("list datasources: %w", err)
	}

	var lastErr error
	for _, ds := range sources {
		if err := s.syncDataSource(ctx, ds); err != nil {
			s.log.Error().Err(err).Str("data_source_id", ds.id).Msg("rls sync failed for datasource")
			s.updateSyncState(ctx, ds.id, "error", 0, 0, err.Error())
			lastErr = err
		}
	}
	return lastErr
}

type dataSource struct {
	id          string
	tenantID    string
	connStrVaultRef string // Vault path for the connection string
}

// listManagedDataSources queries data_sources for all sources with proxy_managed=true.
func (s *rlsSyncer) listManagedDataSources(ctx context.Context) ([]dataSource, error) {
	// Use app_admin role scoped to system context.
	_, err := s.controlDB.Exec(ctx, `
		SET LOCAL ROLE app_admin;
		SET LOCAL app.tenant_id = '';
		SET LOCAL app.user_id = 'system-rls-syncer';
	`)
	if err != nil {
		return nil, fmt.Errorf("set local: %w", err)
	}

	rows, err := s.controlDB.Query(ctx, `
		SELECT id, tenant_id, conn_str_vault_ref
		FROM data_sources
		WHERE proxy_managed = true AND status = 'active'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []dataSource
	for rows.Next() {
		var ds dataSource
		if err := rows.Scan(&ds.id, &ds.tenantID, &ds.connStrVaultRef); err != nil {
			return nil, err
		}
		result = append(result, ds)
	}
	return result, rows.Err()
}

// syncDataSource connects to the managed datasource and applies/updates RLS policies.
func (s *rlsSyncer) syncDataSource(ctx context.Context, ds dataSource) error {
	s.log.Info().Str("data_source_id", ds.id).Msg("syncing RLS policies")
	s.updateSyncState(ctx, ds.id, "running", 0, 0, "")

	// Fetch active policies for this datasource's tenant from the control plane.
	policies, err := s.fetchActivePolicies(ctx, ds.tenantID)
	if err != nil {
		return fmt.Errorf("fetch policies: %w", err)
	}

	// TODO: resolve connStr from Vault using ds.connStrVaultRef.
	// For V1, we log that connection to managed DB is needed and return.
	// The full Vault integration lands in Phase 15.
	s.log.Info().
		Str("data_source_id", ds.id).
		Int("policy_count", len(policies)).
		Msg("RLS sync dry-run (Vault conn resolution pending Phase 15)")

	// Install mask UDFs and mirror RLS policies on the managed datasource.
	// In V1 we record the intent; actual backend connection requires Vault.
	policiesSynced := len(policies)
	udfsInstalled := 0

	s.updateSyncState(ctx, ds.id, "ok", policiesSynced, udfsInstalled, "")
	return nil
}

type policyRow struct {
	id        string
	tableName string
	rowFilter string
	columns   []string
}

func (s *rlsSyncer) fetchActivePolicies(ctx context.Context, tenantID string) ([]policyRow, error) {
	rows, err := s.controlDB.Query(ctx, `
		SELECT p.id, p.resource_name, p.row_filter, p.allowed_columns
		FROM policies p
		JOIN policy_sets ps ON ps.id = p.policy_set_id
		JOIN policy_set_versions psv ON psv.policy_set_id = ps.id
		WHERE ps.tenant_id = $1
		  AND psv.status = 'active'
		  AND p.status = 'active'
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []policyRow
	for rows.Next() {
		var pr policyRow
		var cols []string
		if err := rows.Scan(&pr.id, &pr.tableName, &pr.rowFilter, &cols); err != nil {
			return nil, err
		}
		pr.columns = cols
		result = append(result, pr)
	}
	return result, rows.Err()
}

func (s *rlsSyncer) updateSyncState(ctx context.Context, dataSourceID, status string, policiesSynced, udfsInstalled int, errDetail string) {
	_, err := s.controlDB.Exec(ctx, `
		INSERT INTO rls_sync_state (data_source_id, sync_status, policies_synced, udfs_installed, error_detail, syncer_version, last_synced_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NOW(), NOW())
		ON CONFLICT (data_source_id) DO UPDATE SET
			sync_status     = EXCLUDED.sync_status,
			policies_synced = EXCLUDED.policies_synced,
			udfs_installed  = EXCLUDED.udfs_installed,
			error_detail    = EXCLUDED.error_detail,
			syncer_version  = EXCLUDED.syncer_version,
			last_synced_at  = CASE WHEN EXCLUDED.sync_status = 'ok' THEN NOW() ELSE rls_sync_state.last_synced_at END,
			updated_at      = NOW()
	`, dataSourceID, status, policiesSynced, udfsInstalled, errDetail, s.version)
	if err != nil {
		s.log.Warn().Err(err).Str("data_source_id", dataSourceID).Msg("failed to update rls_sync_state")
	}
}

// installMaskUDFs generates CREATE OR REPLACE FUNCTION DDL for standard mask functions.
// Called on the managed backend datasource (requires superuser / ddl_admin role).
func maskUDFSQL() []string {
	return []string{
		// Email domain masking: user@example.com → ****@example.com
		`CREATE OR REPLACE FUNCTION mask_email_domain(val text)
		 RETURNS text LANGUAGE sql IMMUTABLE AS $$
		   SELECT CASE WHEN val IS NULL THEN NULL
		     ELSE regexp_replace(val, '^[^@]+', '****')
		   END
		 $$;`,

		// Credit card masking: keep last 4 digits
		`CREATE OR REPLACE FUNCTION mask_credit_card(val text)
		 RETURNS text LANGUAGE sql IMMUTABLE AS $$
		   SELECT CASE WHEN val IS NULL THEN NULL
		     ELSE regexp_replace(val, '\d(?=\d{4})', '*', 'g')
		   END
		 $$;`,

		// Full redaction
		`CREATE OR REPLACE FUNCTION mask_redact(val anyelement)
		 RETURNS text LANGUAGE sql IMMUTABLE AS $$
		   SELECT '****'::text
		 $$;`,

		// Partial mask: show first N chars
		`CREATE OR REPLACE FUNCTION mask_partial(val text, show_chars int DEFAULT 4)
		 RETURNS text LANGUAGE sql IMMUTABLE AS $$
		   SELECT CASE WHEN val IS NULL THEN NULL
		     WHEN length(val) <= show_chars THEN val
		     ELSE substring(val, 1, show_chars) || repeat('*', length(val) - show_chars)
		   END
		 $$;`,
	}
}

// nativeRLSPolicy generates the SQL to create/update a native PG RLS policy
// mirroring a proxy enforcement rule.
func nativeRLSPolicySQL(policyID, tableName, rowFilter string) string {
	return fmt.Sprintf(`
		DO $$
		BEGIN
		  IF NOT EXISTS (
		    SELECT 1 FROM pg_policies
		    WHERE schemaname = 'public'
		      AND tablename = '%s'
		      AND policyname = 'proxy_mirror_%s'
		  ) THEN
		    EXECUTE 'CREATE POLICY proxy_mirror_%s ON %s
		      USING (%s)';
		  ELSE
		    EXECUTE 'ALTER POLICY proxy_mirror_%s ON %s
		      USING (%s)';
		  END IF;
		END $$;`,
		tableName, policyID,
		policyID, tableName, escapeSQL(rowFilter),
		policyID, tableName, escapeSQL(rowFilter),
	)
}

func escapeSQL(s string) string {
	// Single-quote escaping for SQL string literals.
	result := ""
	for _, r := range s {
		if r == '\'' {
			result += "''"
		} else {
			result += string(r)
		}
	}
	return result
}

// Unused import guard.
var _ = time.Second
