-- 0016_audit_pipeline_tables.up.sql
-- Phase 5: Audit Pipeline — PostgreSQL-side tables.
-- Adds:
--   • chain_verifications  — hourly verifier run results (compliance evidence)
--   • tenant_audit_keys    — per-tenant HMAC tokenization key references (Vault ARNs)
--   • audit_dlq_replays    — dead-letter-queue replay job tracking
BEGIN;

SET lock_timeout = '5s';

-- ── 1. chain_verifications ───────────────────────────────────────────────────
-- Each row records the outcome of one hash-chain verification run for a tenant.
-- The verifier writes here so compliance teams can query the evidence directly.
CREATE TABLE IF NOT EXISTS chain_verifications (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  verified_at     timestamptz NOT NULL DEFAULT now(),
  period_start    timestamptz NOT NULL,
  period_end      timestamptz NOT NULL,
  scope           text        NOT NULL, -- 'hourly' | 'daily_sample' | 'quarterly_full'
  rows_checked    bigint      NOT NULL DEFAULT 0,
  pg_end_hash     text        NOT NULL, -- last row_hash from policy_audit
  worm_end_hash   text        NOT NULL, -- manifest SHA-256 from WORM object
  matched         boolean     NOT NULL,
  mismatch_detail jsonb,                -- populated only on mismatch
  verifier_version text       NOT NULL,

  CONSTRAINT chain_verifications_pkey      PRIMARY KEY (id),
  CONSTRAINT chain_verifications_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT chain_verifications_scope_ck  CHECK (scope IN ('hourly','daily_sample','quarterly_full')),
  CONSTRAINT chain_verifications_period_ck CHECK (period_start < period_end)
);

CREATE INDEX IF NOT EXISTS chain_verifications_tenant_time_idx
  ON chain_verifications (tenant_id, verified_at DESC);

CREATE INDEX IF NOT EXISTS chain_verifications_unmatched_idx
  ON chain_verifications (tenant_id, verified_at DESC)
  WHERE matched = false;

-- ── 2. tenant_audit_keys ──────────────────────────────────────────────────────
-- Stores references (Vault paths / KMS ARNs) to the per-tenant HMAC keys used
-- for GDPR-safe actor tokenisation. Destroying the key makes tokens unlinkable.
CREATE TABLE IF NOT EXISTS tenant_audit_keys (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  key_ref         text        NOT NULL, -- e.g. "secret/audit/tokens/<tenant_id>"
  key_arn         text,                 -- AWS KMS ARN if using external KMS
  algorithm       text        NOT NULL DEFAULT 'HMAC-SHA256',
  status          text        NOT NULL DEFAULT 'active',
  rotated_at      timestamptz,
  destroyed_at    timestamptz,          -- set on GDPR key destruction
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid        NOT NULL,

  CONSTRAINT tenant_audit_keys_pkey      PRIMARY KEY (id),
  CONSTRAINT tenant_audit_keys_tenant_fk FOREIGN KEY (tenant_id)  REFERENCES tenants(id),
  CONSTRAINT tenant_audit_keys_user_fk   FOREIGN KEY (created_by) REFERENCES users(id),
  CONSTRAINT tenant_audit_keys_status_ck CHECK (status IN ('active','rotated','destroyed')),
  CONSTRAINT tenant_audit_keys_algo_ck   CHECK (algorithm IN ('HMAC-SHA256'))
);

-- Only one active key per tenant at a time.
CREATE UNIQUE INDEX IF NOT EXISTS tenant_audit_keys_active_uq
  ON tenant_audit_keys (tenant_id)
  WHERE status = 'active';

-- ── 3. audit_dlq_replays ──────────────────────────────────────────────────────
-- Tracks manual DLQ replay jobs so operators can audit who replayed what.
CREATE TABLE IF NOT EXISTS audit_dlq_replays (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid,                 -- null = cross-tenant system-level replay
  consumer        text        NOT NULL, -- 'clickhouse-sink' | 'worm-sink' | 'chain-verifier'
  topic           text        NOT NULL,
  partition_key   text,
  from_offset     bigint,
  to_offset       bigint,
  initiated_by    uuid        NOT NULL,
  initiated_at    timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  events_replayed bigint      NOT NULL DEFAULT 0,
  status          text        NOT NULL DEFAULT 'pending',
  error_detail    text,

  CONSTRAINT audit_dlq_replays_pkey        PRIMARY KEY (id),
  CONSTRAINT audit_dlq_replays_user_fk     FOREIGN KEY (initiated_by) REFERENCES users(id),
  CONSTRAINT audit_dlq_replays_status_ck   CHECK (status IN ('pending','running','completed','failed')),
  CONSTRAINT audit_dlq_replays_consumer_ck CHECK (consumer IN ('clickhouse-sink','worm-sink','chain-verifier'))
);

CREATE INDEX IF NOT EXISTS audit_dlq_replays_consumer_idx
  ON audit_dlq_replays (consumer, initiated_at DESC);

-- ── 4. RLS ───────────────────────────────────────────────────────────────────
ALTER TABLE chain_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE chain_verifications FORCE ROW LEVEL SECURITY;
CREATE POLICY chain_verifications_tenant_iso ON chain_verifications
  USING (tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE tenant_audit_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_audit_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_audit_keys_tenant_iso ON tenant_audit_keys
  USING (tenant_id::text = current_setting('app.tenant_id', true));

-- audit_dlq_replays: admin-only table; app_admin can bypass tenant isolation.
ALTER TABLE audit_dlq_replays ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_dlq_replays FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_dlq_replays_admin_iso ON audit_dlq_replays
  USING (
    tenant_id IS NULL
    OR tenant_id::text = current_setting('app.tenant_id', true)
  );

-- ── 5. Grants ─────────────────────────────────────────────────────────────────
GRANT SELECT                         ON chain_verifications TO app_read;
GRANT SELECT, INSERT                 ON chain_verifications TO app_write;
GRANT SELECT, INSERT, UPDATE         ON chain_verifications TO app_admin;
GRANT ALL PRIVILEGES                 ON chain_verifications TO app_migrator;

GRANT SELECT                         ON tenant_audit_keys   TO app_read;
GRANT SELECT, INSERT, UPDATE         ON tenant_audit_keys   TO app_write;
GRANT SELECT, INSERT, UPDATE         ON tenant_audit_keys   TO app_admin;
GRANT ALL PRIVILEGES                 ON tenant_audit_keys   TO app_migrator;

GRANT SELECT                         ON audit_dlq_replays   TO app_read;
GRANT SELECT, INSERT, UPDATE         ON audit_dlq_replays   TO app_write;
GRANT SELECT, INSERT, UPDATE         ON audit_dlq_replays   TO app_admin;
GRANT ALL PRIVILEGES                 ON audit_dlq_replays   TO app_migrator;

COMMIT;
