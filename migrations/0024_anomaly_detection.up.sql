-- Phase 13: Anomaly Detection & Risk-Aware ABAC
-- Forward-only migration; no down migration in production.

-- ── risk_scores ────────────────────────────────────────────────────────────────
-- Hot store is Redis (TTL 60s). This table is the durable record for history,
-- replay, and SIEM export. Partitioned by day for efficient pruning.
CREATE TABLE IF NOT EXISTS risk_scores (
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       uuid        NOT NULL,
    user_id         uuid        NOT NULL,
    score           smallint    NOT NULL CHECK (score BETWEEN 0 AND 100),
    components      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    decayed_total   smallint    NOT NULL CHECK (decayed_total BETWEEN 0 AND 100),
    model_version   text        NOT NULL DEFAULT 'stat-v1.0.0',
    computed_at     timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

-- Seed the current-month partition; pg_partman will manage future ones.
CREATE TABLE IF NOT EXISTS risk_scores_default
    PARTITION OF risk_scores DEFAULT;

ALTER TABLE risk_scores ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_scores FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_scores_tenant_isolation ON risk_scores
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_risk_scores_tenant_user_at
    ON risk_scores (tenant_id, user_id, computed_at DESC);

-- ── risk_baselines ─────────────────────────────────────────────────────────────
-- Per-user statistical baseline state persisted for durability between restarts.
-- The anomaly-detector service keeps a hot in-memory copy; this is the checkpoint.
CREATE TABLE IF NOT EXISTS risk_baselines (
    tenant_id           uuid        NOT NULL,
    user_id             uuid        NOT NULL,
    hour_histogram      jsonb       NOT NULL DEFAULT '{}',   -- 24-bin counts
    dow_histogram       jsonb       NOT NULL DEFAULT '{}',   -- 7-bin counts
    resource_set        jsonb       NOT NULL DEFAULT '[]',   -- array of resource hashes
    row_count_quantiles jsonb       NOT NULL DEFAULT '{}',   -- p50/p90/p99
    ip_set              jsonb       NOT NULL DEFAULT '[]',   -- array of IP hashes
    datasource_set      jsonb       NOT NULL DEFAULT '[]',   -- data source IDs
    event_count         bigint      NOT NULL DEFAULT 0,
    warm_up_done        boolean     NOT NULL DEFAULT false,
    last_updated_at     timestamptz NOT NULL DEFAULT now(),
    created_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

ALTER TABLE risk_baselines ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_baselines FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_baselines_tenant_isolation ON risk_baselines
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

-- ── risk_allowlists ────────────────────────────────────────────────────────────
-- Service accounts, batch jobs, and known-good principals that bypass scoring.
CREATE TABLE IF NOT EXISTS risk_allowlists (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id       uuid        NOT NULL,
    principal_id    uuid        NOT NULL,         -- user_id or service account ID
    reason          text        NOT NULL,
    until_at        timestamptz,                  -- NULL = permanent
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE risk_allowlists ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_allowlists FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_allowlists_tenant_isolation ON risk_allowlists
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_risk_allowlists_tenant_principal
    ON risk_allowlists (tenant_id, principal_id)
    WHERE (until_at IS NULL OR until_at > now());

-- ── risk_calibrations ──────────────────────────────────────────────────────────
-- Per-tenant weight + threshold configuration. Immutable versioned rows.
CREATE TABLE IF NOT EXISTS risk_calibrations (
    id          uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    weights     jsonb       NOT NULL DEFAULT '{"time_of_day":0.25,"day_of_week":0.10,"resource_novelty":0.25,"row_volume":0.20,"ip_drift":0.20}'::jsonb,
    thresholds  jsonb       NOT NULL DEFAULT '{"low":40,"medium":70,"high":85}'::jsonb,
    version     integer     NOT NULL DEFAULT 1,
    created_by  uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE risk_calibrations ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_calibrations FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_calibrations_tenant_isolation ON risk_calibrations
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_risk_calibrations_tenant_version
    ON risk_calibrations (tenant_id, version DESC);

-- ── risk_events ────────────────────────────────────────────────────────────────
-- Scored spikes, manual overrides, and decay events. Feeds Live Activity and
-- Phase 12 webhooks via the risk.spike event type.
CREATE TABLE IF NOT EXISTS risk_events (
    id          uuid        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    user_id     uuid        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('spike','decay','override','warmup_end')),
    score_before smallint,
    score_after  smallint,
    payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    actor_id    uuid,                              -- NULL for system-generated events
    created_at  timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS risk_events_default
    PARTITION OF risk_events DEFAULT;

ALTER TABLE risk_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_events FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_events_tenant_isolation ON risk_events
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_risk_events_tenant_user_at
    ON risk_events (tenant_id, user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_risk_events_tenant_kind_at
    ON risk_events (tenant_id, kind, created_at DESC);

-- ── RBAC grants ────────────────────────────────────────────────────────────────
GRANT SELECT, INSERT ON risk_scores         TO app_user;
GRANT SELECT, INSERT, UPDATE ON risk_baselines TO app_user;
GRANT SELECT, INSERT ON risk_allowlists     TO app_user;
GRANT SELECT, INSERT ON risk_calibrations   TO app_user;
GRANT SELECT, INSERT ON risk_events         TO app_user;
