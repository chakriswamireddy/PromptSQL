-- Phase 15: Scale-Out — Kubernetes, HA, Multi-Region
-- Forward-only migration. No down migration in production.

-- ── Add region columns to tenants ─────────────────────────────────────────────
-- home_region: where writes (policy mutations, auth) are routed.
-- data_residency: legal/compliance constraint; enforced at the routing layer.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                 WHERE table_name = 'tenants' AND column_name = 'home_region') THEN
    ALTER TABLE tenants
      ADD COLUMN home_region     text        NOT NULL DEFAULT 'us-east-1'
                                 CHECK (home_region IN ('us-east-1','eu-west-1','ap-southeast-1')),
      ADD COLUMN data_residency  text        NOT NULL DEFAULT 'us'
                                 CHECK (data_residency IN ('us','eu','apac','multi'));
  END IF;
END $$;

COMMENT ON COLUMN tenants.home_region IS 'AWS region that owns the write endpoint for this tenant';
COMMENT ON COLUMN tenants.data_residency IS 'Data residency constraint: us | eu | apac | multi';

-- ── region_routing_log — audits cross-region routing decisions ─────────────────
CREATE TABLE IF NOT EXISTS region_routing_log (
    id              bigserial   PRIMARY KEY,
    tenant_id       uuid        NOT NULL,
    request_id      text        NOT NULL,
    source_region   text        NOT NULL,
    target_region   text        NOT NULL,
    route_reason    text        NOT NULL,  -- 'home_region' | 'data_residency' | 'write_affinity'
    latency_ms      integer,
    created_at      timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

-- Retain 30 days of routing logs in daily partitions.
CREATE TABLE IF NOT EXISTS region_routing_log_2026_01
    PARTITION OF region_routing_log
    FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');

CREATE TABLE IF NOT EXISTS region_routing_log_2026_05
    PARTITION OF region_routing_log
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');

CREATE TABLE IF NOT EXISTS region_routing_log_2026_06
    PARTITION OF region_routing_log
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE INDEX IF NOT EXISTS region_routing_log_tenant_created
    ON region_routing_log (tenant_id, created_at DESC);

-- RLS: tenants can only read their own routing log.
ALTER TABLE region_routing_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE region_routing_log FORCE ROW LEVEL SECURITY;

CREATE POLICY region_routing_log_tenant_isolation ON region_routing_log
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── dr_drills — records of DR drill execution results ─────────────────────────
CREATE TABLE IF NOT EXISTS dr_drills (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    drill_type      text        NOT NULL
                    CHECK (drill_type IN ('pg_failover','kafka_failover','clickhouse_loss','worm_compromise','vault_unavailable','full_regional')),
    environment     text        NOT NULL CHECK (environment IN ('staging','prod')),
    executed_by     text        NOT NULL,
    started_at      timestamptz NOT NULL,
    completed_at    timestamptz,
    rto_minutes     numeric(6,2),
    rpo_minutes     numeric(6,2),
    rto_target_met  boolean,
    rpo_target_met  boolean,
    notes           text,
    artifacts_url   text,        -- link to runbook execution log / recording
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dr_drills_environment_started
    ON dr_drills (environment, started_at DESC);

COMMENT ON TABLE dr_drills IS 'Mandatory quarterly DR drill log. Both rto_target_met AND rpo_target_met must be true before Phase 15 can be marked GA-ready.';

-- ── replication_lag_snapshots — for alerting and failover-readiness scorecard ──
CREATE TABLE IF NOT EXISTS replication_lag_snapshots (
    id              bigserial   PRIMARY KEY,
    source_region   text        NOT NULL,
    target_region   text        NOT NULL,
    component       text        NOT NULL CHECK (component IN ('aurora','kafka','flink_checkpoints','worm_s3')),
    lag_seconds     numeric(10,3),
    snapshot_at     timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (snapshot_at);

CREATE TABLE IF NOT EXISTS replication_lag_snapshots_2026_05
    PARTITION OF replication_lag_snapshots
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');

CREATE TABLE IF NOT EXISTS replication_lag_snapshots_2026_06
    PARTITION OF replication_lag_snapshots
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE INDEX IF NOT EXISTS replication_lag_component_time
    ON replication_lag_snapshots (component, snapshot_at DESC);
