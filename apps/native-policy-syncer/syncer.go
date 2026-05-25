package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pkgaudit "github.com/governance-platform/pkg/audit"
	"github.com/governance-platform/pkg/connectors"
	"github.com/governance-platform/pkg/featureflags"
)

// dataSourceRecord holds the fields the syncer reads from the data_sources table.
type dataSourceRecord struct {
	ID          string
	Engine      string
	SecretRef   string
	Database    string
	Schema      string
	TenantID    string
	PolicySetVersion string
}

// policyRecord holds a platform policy row that maps to a NativePolicy.
type policyRecord struct {
	ID            string
	TableSchema   string
	TableName     string
	ColumnName    string
	MaskKind      string
	RowFilter     string
	PolicyVersion string
}

// Syncer coordinates native policy sync across all enabled data sources.
type Syncer struct {
	pool        *pgxpool.Pool
	auditClient *pkgaudit.Client
	tracer      trace.Tracer
	log         zerolog.Logger
	cfg         config
	metrics     *syncMetrics
	ff          *featureflags.Client
}

func newSyncer(
	pool *pgxpool.Pool,
	auditClient *pkgaudit.Client,
	tracer trace.Tracer,
	log zerolog.Logger,
	cfg config,
	metrics *syncMetrics,
	ff *featureflags.Client,
) *Syncer {
	return &Syncer{
		pool:        pool,
		auditClient: auditClient,
		tracer:      tracer,
		log:         log,
		cfg:         cfg,
		metrics:     metrics,
		ff:          ff,
	}
}

// RunLoop runs the periodic sync on the configured interval until ctx is done.
// Each tick syncs all enabled data sources concurrently up to SyncConcurrency.
func (s *Syncer) RunLoop(ctx context.Context) {
	s.log.Info().Dur("interval", s.cfg.SyncInterval).Msg("syncer: starting background loop")
	ticker := time.NewTicker(s.cfg.SyncInterval)
	defer ticker.Stop()

	// Run once immediately at startup.
	s.runAllDataSources(ctx)

	for {
		select {
		case <-ticker.C:
			s.runAllDataSources(ctx)
		case <-ctx.Done():
			s.log.Info().Msg("syncer: background loop stopped")
			return
		}
	}
}

// runAllDataSources fetches all enabled data sources and syncs them concurrently.
func (s *Syncer) runAllDataSources(ctx context.Context) {
	_, span := s.tracer.Start(ctx, "syncer.runAllDataSources")
	defer span.End()

	sources, err := s.listEnabledDataSources(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("syncer: failed to list data sources")
		return
	}
	s.log.Info().Int("count", len(sources)).Msg("syncer: syncing data sources")

	sem := make(chan struct{}, s.cfg.SyncConcurrency)
	var wg sync.WaitGroup
	for _, ds := range sources {
		// Check per-engine feature flag.
		engineFlag, ok := s.cfg.PerEngineFlags[ds.Engine]
		if !ok || !s.ff.IsEnabled(engineFlag) {
			s.log.Debug().Str("engine", ds.Engine).Str("data_source_id", ds.ID).
				Msg("syncer: engine flag disabled, skipping")
			continue
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(ds dataSourceRecord) {
			defer wg.Done()
			defer func() { <-sem }()

			srcCtx, cancel := context.WithTimeout(ctx, s.cfg.SyncTimeoutPerSource)
			defer cancel()

			if err := s.SyncDataSource(srcCtx, ds.ID); err != nil {
				s.log.Error().Err(err).
					Str("data_source_id", ds.ID).
					Str("engine", ds.Engine).
					Msg("syncer: sync failed")
			}
		}(ds)
	}
	wg.Wait()
}

// SyncDataSource performs a full policy sync for a single data source.
// It is idempotent: if the stored sync_version matches the current
// policy_set_version, the sync is skipped.
func (s *Syncer) SyncDataSource(ctx context.Context, dataSourceID string) error {
	ctx, span := s.tracer.Start(ctx, "syncer.SyncDataSource",
		trace.WithAttributes(attribute.String("data_source_id", dataSourceID)))
	defer span.End()

	// 1. Load data source record.
	ds, err := s.loadDataSource(ctx, dataSourceID)
	if err != nil {
		return fmt.Errorf("load data source: %w", err)
	}

	// 2. Check per-engine flag.
	engineFlag, ok := s.cfg.PerEngineFlags[ds.Engine]
	if !ok || !s.ff.IsEnabled(engineFlag) {
		s.log.Info().Str("engine", ds.Engine).Msg("syncer: engine flag disabled")
		return nil
	}

	// 3. Check idempotency: skip if sync_version matches.
	if skip, err := s.shouldSkip(ctx, ds); err != nil {
		s.log.Warn().Err(err).Msg("syncer: idempotency check failed (proceeding)")
	} else if skip {
		s.metrics.syncSkipped.WithLabelValues(ds.Engine).Inc()
		s.log.Debug().Str("data_source_id", ds.ID).Msg("syncer: version unchanged, skipping")
		return nil
	}

	start := time.Now()

	// 4. Resolve DSN from Vault.
	dsn, err := s.resolveDSN(ctx, ds.SecretRef)
	if err != nil {
		s.metrics.connectorUp.WithLabelValues(ds.Engine, ds.ID).Set(0)
		s.metrics.syncErrors.WithLabelValues(ds.Engine, "vault").Inc()
		_ = s.writeEnforcementLog(context.Background(), ds, "apply", "error",
			map[string]any{"error": err.Error()})
		return fmt.Errorf("resolve dsn for %s: %w", ds.ID, err)
	}

	// 5. Build connector.
	connector, err := connectors.NewConnector(ctx, &connectors.DataSource{
		ID:        ds.ID,
		Engine:    connectors.Engine(ds.Engine),
		DSN:       dsn,
		Database:  ds.Database,
		Schema:    ds.Schema,
		SecretRef: ds.SecretRef,
	}, s.log, s.tracer)
	if err != nil {
		s.metrics.syncErrors.WithLabelValues(ds.Engine, "factory").Inc()
		return fmt.Errorf("create connector: %w", err)
	}
	defer connector.Close() //nolint:errcheck

	// 6. Connect.
	if err := connector.Connect(ctx, &connectors.DataSource{
		ID:        ds.ID,
		Engine:    connectors.Engine(ds.Engine),
		DSN:       dsn,
		Database:  ds.Database,
		Schema:    ds.Schema,
		SecretRef: ds.SecretRef,
	}); err != nil {
		s.metrics.connectorUp.WithLabelValues(ds.Engine, ds.ID).Set(0)
		s.metrics.syncErrors.WithLabelValues(ds.Engine, "connect").Inc()
		_ = s.writeEnforcementLog(context.Background(), ds, "apply", "error",
			map[string]any{"error": err.Error(), "stage": "connect"})
		return fmt.Errorf("connect %s %s: %w", ds.Engine, ds.ID, err)
	}
	s.metrics.connectorUp.WithLabelValues(ds.Engine, ds.ID).Set(1)

	// 7. PrepareUDFs (idempotent).
	if err := connector.PrepareUDFs(ctx); err != nil {
		s.log.Warn().Err(err).Str("data_source_id", ds.ID).Msg("syncer: prepare UDFs failed (non-fatal)")
	}

	// 8. Load active policies for this data source.
	policies, err := s.loadActivePolicies(ctx, ds.TenantID, ds.ID)
	if err != nil {
		s.metrics.syncErrors.WithLabelValues(ds.Engine, "load_policies").Inc()
		return fmt.Errorf("load policies for %s: %w", ds.ID, err)
	}

	// 9. Sync native policies.
	result, err := connector.SyncNativePolicies(ctx, policies)
	if err != nil {
		s.metrics.syncErrors.WithLabelValues(ds.Engine, "sync").Inc()
		_ = s.writeEnforcementLog(context.Background(), ds, "apply", "error",
			map[string]any{"error": err.Error()})
		return fmt.Errorf("sync native policies for %s: %w", ds.ID, err)
	}

	// 10. Record metrics.
	dur := time.Since(start)
	status := "ok"
	if result.PoliciesErr > 0 {
		status = "partial"
	}
	s.metrics.syncDuration.WithLabelValues(ds.Engine, status).Observe(dur.Seconds())
	s.metrics.policiesSynced.WithLabelValues(ds.Engine).Add(float64(result.PoliciesOK))
	s.metrics.lastSyncAge.WithLabelValues(ds.Engine, ds.ID).Set(0)

	// 11. Write enforcement log.
	logDetail := map[string]any{
		"policies_total": result.PoliciesTotal,
		"policies_ok":    result.PoliciesOK,
		"policies_err":   result.PoliciesErr,
		"duration_ms":    dur.Milliseconds(),
	}
	if result.PoliciesErr > 0 {
		errs := make([]string, 0, len(result.Errors))
		for _, e := range result.Errors {
			errs = append(errs, e.Error())
		}
		logDetail["errors"] = errs
	}
	_ = s.writeEnforcementLog(context.Background(), ds, "apply", status, logDetail)

	// 12. Update engine_sync_state.
	if err := s.updateSyncState(context.Background(), ds, ds.PolicySetVersion, nil); err != nil {
		s.log.Error().Err(err).Str("data_source_id", ds.ID).Msg("syncer: update sync state failed")
	}

	// 13. Audit event.
	s.auditClient.SystemEvent(ctx, pkgaudit.SystemEvent{
		TenantID: ds.TenantID,
		Action:   "native_policy.sync",
		Detail:   logDetail,
	})

	s.log.Info().
		Str("data_source_id", ds.ID).
		Str("engine", ds.Engine).
		Int("ok", result.PoliciesOK).
		Int("err", result.PoliciesErr).
		Dur("duration", dur).
		Msg("syncer: sync complete")

	return nil
}

// shouldSkip returns true if the stored sync_version matches the current policy_set_version.
func (s *Syncer) shouldSkip(ctx context.Context, ds dataSourceRecord) (bool, error) {
	var storedVersion *string
	err := s.pool.QueryRow(ctx, `
		SELECT sync_version
		FROM engine_sync_state
		WHERE tenant_id = $1 AND data_source_id = $2 AND engine = $3 AND sync_kind = 'native_policy'
	`, ds.TenantID, ds.ID, ds.Engine).Scan(&storedVersion)
	if err != nil {
		return false, nil // not found → do not skip
	}
	if storedVersion == nil {
		return false, nil
	}
	return *storedVersion == ds.PolicySetVersion, nil
}

// listEnabledDataSources fetches all data sources that have the multi-db engine.
func (s *Syncer) listEnabledDataSources(ctx context.Context) ([]dataSourceRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			ds.id::text,
			ds.engine,
			ds.secret_ref,
			COALESCE(ds.database_name, '') AS database_name,
			COALESCE(ds.schema_name, '') AS schema_name,
			ds.tenant_id::text,
			COALESCE(psv.version, '') AS policy_set_version
		FROM data_sources ds
		LEFT JOIN policy_set_versions psv
		  ON psv.tenant_id = ds.tenant_id AND psv.is_active = TRUE
		WHERE ds.is_active = TRUE
		  AND ds.engine IN ('postgres','mysql','sqlserver','oracle','snowflake','bigquery','databricks','mongodb')
		ORDER BY ds.tenant_id, ds.id
	`)
	if err != nil {
		return nil, fmt.Errorf("list data sources: %w", err)
	}
	defer rows.Close()

	var out []dataSourceRecord
	for rows.Next() {
		var r dataSourceRecord
		if err := rows.Scan(&r.ID, &r.Engine, &r.SecretRef, &r.Database, &r.Schema,
			&r.TenantID, &r.PolicySetVersion); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadDataSource fetches a single data source by ID.
func (s *Syncer) loadDataSource(ctx context.Context, dataSourceID string) (dataSourceRecord, error) {
	var r dataSourceRecord
	err := s.pool.QueryRow(ctx, `
		SELECT
			ds.id::text,
			ds.engine,
			ds.secret_ref,
			COALESCE(ds.database_name, '') AS database_name,
			COALESCE(ds.schema_name, '') AS schema_name,
			ds.tenant_id::text,
			COALESCE(psv.version, '') AS policy_set_version
		FROM data_sources ds
		LEFT JOIN policy_set_versions psv
		  ON psv.tenant_id = ds.tenant_id AND psv.is_active = TRUE
		WHERE ds.id = $1
	`, dataSourceID,
	).Scan(&r.ID, &r.Engine, &r.SecretRef, &r.Database, &r.Schema,
		&r.TenantID, &r.PolicySetVersion)
	if err != nil {
		return r, fmt.Errorf("load data source %s: %w", dataSourceID, err)
	}
	return r, nil
}

// loadActivePolicies fetches active policies for a data source and converts
// them to the connectors.NativePolicy slice.
func (s *Syncer) loadActivePolicies(ctx context.Context, tenantID, dataSourceID string) ([]*connectors.NativePolicy, error) {
	// This query joins the policy tables from the control plane.
	// The exact schema depends on Phase 3's policy tables; we use the generic form.
	rows, err := s.pool.Query(ctx, `
		SELECT
			p.id::text,
			COALESCE(p.table_schema, '') AS table_schema,
			COALESCE(p.table_name, '') AS table_name,
			COALESCE(p.column_name, '') AS column_name,
			COALESCE(p.mask_kind, 'null') AS mask_kind,
			COALESCE(p.row_filter, '') AS row_filter,
			COALESCE(psv.version, p.id::text) AS policy_version
		FROM policies p
		JOIN policy_set_versions psv
		  ON psv.id = p.policy_set_version_id
		WHERE p.tenant_id = $1
		  AND p.data_source_id = $2
		  AND p.is_active = TRUE
		  AND psv.is_active = TRUE
		ORDER BY p.id
	`, tenantID, dataSourceID)
	if err != nil {
		return nil, fmt.Errorf("load policies: %w", err)
	}
	defer rows.Close()

	var out []*connectors.NativePolicy
	for rows.Next() {
		var r policyRecord
		if err := rows.Scan(&r.ID, &r.TableSchema, &r.TableName, &r.ColumnName,
			&r.MaskKind, &r.RowFilter, &r.PolicyVersion); err != nil {
			return nil, err
		}
		out = append(out, &connectors.NativePolicy{
			TableSchema:   r.TableSchema,
			TableName:     r.TableName,
			ColumnName:    r.ColumnName,
			MaskKind:      r.MaskKind,
			RowFilter:     r.RowFilter,
			PolicyID:      r.ID,
			PolicyVersion: r.PolicyVersion,
		})
	}
	return out, rows.Err()
}

// writeEnforcementLog inserts a row into native_enforcement_log.
func (s *Syncer) writeEnforcementLog(ctx context.Context, ds dataSourceRecord, operation, status string, details map[string]any) error {
	detailJSON, err := json.Marshal(details)
	if err != nil {
		detailJSON = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO native_enforcement_log
		  (tenant_id, data_source_id, engine, operation, status, details, sync_version)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7)
	`, ds.TenantID, ds.ID, ds.Engine, operation, status, string(detailJSON), ds.PolicySetVersion)
	return err
}

// updateSyncState upserts the engine_sync_state row for a data source.
func (s *Syncer) updateSyncState(ctx context.Context, ds dataSourceRecord, syncVersion string, lastErr error) error {
	var errText *string
	if lastErr != nil {
		e := lastErr.Error()
		errText = &e
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO engine_sync_state
		  (tenant_id, data_source_id, engine, sync_kind, last_synced_at, last_error, sync_version, updated_at)
		VALUES ($1::uuid, $2::uuid, $3, 'native_policy', NOW(), $4, $5, NOW())
		ON CONFLICT (tenant_id, data_source_id, engine, sync_kind)
		DO UPDATE SET
		  last_synced_at = NOW(),
		  last_error     = EXCLUDED.last_error,
		  sync_version   = EXCLUDED.sync_version,
		  updated_at     = NOW()
	`, ds.TenantID, ds.ID, ds.Engine, errText, syncVersion)
	return err
}

// resolveDSN retrieves the DSN for a data source from Vault.
// In production, this calls the Vault agent sidecar.
// In local/dev mode it reads from env var DATASOURCE_DSN_<secretRef>.
func (s *Syncer) resolveDSN(ctx context.Context, secretRef string) (string, error) {
	// Dev: env var override.
	envKey := "DATASOURCE_DSN_" + secretRef
	if dsn := os.Getenv(envKey); dsn != "" {
		return dsn, nil
	}
	if dsn := os.Getenv("DATASOURCE_DSN_DEFAULT"); dsn != "" {
		s.log.Warn().Str("secret_ref", secretRef).Msg("using DATASOURCE_DSN_DEFAULT (dev mode)")
		return dsn, nil
	}
	// Production: call Vault.
	// TODO: implement Vault agent read via HTTP to cfg.VaultAddr.
	// Placeholder returns an error to surface missing configuration early.
	return "", fmt.Errorf("no DSN found for secret_ref %q; set DATASOURCE_DSN_%s for dev mode", secretRef, secretRef)
}
