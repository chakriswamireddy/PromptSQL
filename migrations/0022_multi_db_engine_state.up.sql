-- Phase 11: Multi-Database Expansion — engine state tracking
-- Tables: engine_sync_state, native_enforcement_log
-- Extends:  data_sources (engine_capabilities column)
-- All tenant-scoped tables enforce RLS FORCE.

BEGIN;

-- ─── Extend data_sources ─────────────────────────────────────────────────────
-- Add capabilities JSON so the syncer can introspect what each engine supports
-- without round-tripping to the engine itself.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'data_sources' AND column_name = 'engine_capabilities'
    ) THEN
        ALTER TABLE data_sources
            ADD COLUMN engine_capabilities JSONB NOT NULL DEFAULT '{}';
    END IF;
END;
$$;

COMMENT ON COLUMN data_sources.engine_capabilities IS
    'Immutable capability map produced by the connector on first connect, '
    'e.g. {"row_filter":true,"column_mask":true,"native_rls":false,"ddm":true}';

-- ─── engine_sync_state ───────────────────────────────────────────────────────
-- One row per (tenant, data_source, engine, sync_kind).
-- The native-policy-syncer updates this row after every sync attempt so it can
-- detect staleness and skip re-syncs when sync_version has not changed.

CREATE TABLE IF NOT EXISTS engine_sync_state (
    id              UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id       UUID        NOT NULL,
    data_source_id  UUID        NOT NULL,
    engine          TEXT        NOT NULL
                        CHECK (engine IN (
                            'postgres','mysql','sqlserver','oracle',
                            'snowflake','bigquery','databricks','mongodb'
                        )),
    sync_kind       TEXT        NOT NULL
                        CHECK (sync_kind IN ('native_policy','crawler','schema')),
    last_synced_at  TIMESTAMPTZ,
    last_error      TEXT,
    -- Opaque version tag matching the policy_set_version that was applied.
    -- If the current policy_set_version equals sync_version, skip sync.
    sync_version    TEXT,
    metadata        JSONB       NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT engine_sync_state_unique
        UNIQUE (tenant_id, data_source_id, engine, sync_kind)
);

CREATE INDEX IF NOT EXISTS engine_sync_state_tenant
    ON engine_sync_state (tenant_id, data_source_id);

CREATE INDEX IF NOT EXISTS engine_sync_state_stale
    ON engine_sync_state (last_synced_at)
    WHERE last_synced_at IS NOT NULL;

ALTER TABLE engine_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE engine_sync_state FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON engine_sync_state;
CREATE POLICY tenant_isolation ON engine_sync_state
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ─── native_enforcement_log ──────────────────────────────────────────────────
-- Append-only audit log of every native-policy operation (apply/revert/verify).
-- Partitioned monthly by created_at for efficient purge and ClickHouse ingestion.
-- Aligns with the audit pipeline pattern in Phase 5.

CREATE TABLE IF NOT EXISTS native_enforcement_log (
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    data_source_id  UUID        NOT NULL,
    engine          TEXT        NOT NULL
                        CHECK (engine IN (
                            'postgres','mysql','sqlserver','oracle',
                            'snowflake','bigquery','databricks','mongodb'
                        )),
    operation       TEXT        NOT NULL
                        CHECK (operation IN ('apply','revert','verify')),
    status          TEXT        NOT NULL
                        CHECK (status IN ('ok','error','skipped')),
    -- JSON detail: policy IDs applied, error messages, affected objects, etc.
    details         JSONB       NOT NULL DEFAULT '{}',
    -- sync_version ties the log row to the policy_set_version that was synced.
    sync_version    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Seed initial monthly partitions covering the next 6 months.
-- The syncer creates future partitions automatically; these cover bootstrap.
DO $$
DECLARE
    month_start DATE;
    month_end   DATE;
    part_name   TEXT;
BEGIN
    FOR i IN 0..5 LOOP
        month_start := DATE_TRUNC('month', NOW()) + (i * INTERVAL '1 month');
        month_end   := month_start + INTERVAL '1 month';
        part_name   := 'native_enforcement_log_' || TO_CHAR(month_start, 'YYYY_MM');
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            WHERE c.relname = part_name AND n.nspname = 'public'
        ) THEN
            EXECUTE format(
                'CREATE TABLE IF NOT EXISTS %I '
                'PARTITION OF native_enforcement_log '
                'FOR VALUES FROM (%L) TO (%L)',
                part_name, month_start, month_end
            );
        END IF;
    END LOOP;
END;
$$;

CREATE INDEX IF NOT EXISTS native_enforcement_log_tenant
    ON native_enforcement_log (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS native_enforcement_log_datasource
    ON native_enforcement_log (data_source_id, created_at DESC);

CREATE INDEX IF NOT EXISTS native_enforcement_log_engine_status
    ON native_enforcement_log (engine, status, created_at DESC);

ALTER TABLE native_enforcement_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE native_enforcement_log FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON native_enforcement_log;
CREATE POLICY tenant_isolation ON native_enforcement_log
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

COMMIT;
