-- 0017_proxy_tables.up.sql
-- Phase 6: PEP PostgreSQL Proxy — control-plane tables.
-- Adds:
--   • proxy_session_tokens  — PG forensics mirror of Redis token→SessionContext bindings
--   • rls_sync_state        — per-datasource native RLS mirror sync status
--   • proxy_query_log       — lightweight per-query attribution for admin inspector
BEGIN;

SET lock_timeout = '5s';

-- ── 1. proxy_session_tokens ───────────────────────────────────────────────────
-- Primary store is Redis (TTL 15 min). This table is a forensics mirror written
-- async by the token issuer. Never read on the hot path; used for audit forensics
-- and incident investigation.
CREATE TABLE IF NOT EXISTS proxy_session_tokens (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  user_id         uuid        NOT NULL,
  token_hash      text        NOT NULL,  -- sha256(token) — raw token never stored
  session_id      uuid        NOT NULL,
  data_source_id  uuid,                  -- null = any datasource
  issued_at       timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,
  revoked_at      timestamptz,
  client_ip       text,
  user_agent      text,

  CONSTRAINT proxy_session_tokens_pkey      PRIMARY KEY (id),
  CONSTRAINT proxy_session_tokens_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT proxy_session_tokens_user_fk   FOREIGN KEY (user_id)   REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS proxy_session_tokens_hash_idx
  ON proxy_session_tokens (token_hash)
  WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS proxy_session_tokens_user_idx
  ON proxy_session_tokens (user_id, issued_at DESC);

CREATE INDEX IF NOT EXISTS proxy_session_tokens_expire_idx
  ON proxy_session_tokens (expires_at)
  WHERE revoked_at IS NULL;

-- ── 2. rls_sync_state ─────────────────────────────────────────────────────────
-- Tracks the last successful RLS policy sync for each managed datasource.
-- The proxy-rls-syncer writes here after each hourly run.
CREATE TABLE IF NOT EXISTS rls_sync_state (
  data_source_id  uuid        NOT NULL,
  last_synced_at  timestamptz,
  sync_status     text        NOT NULL DEFAULT 'pending',
  policies_synced int         NOT NULL DEFAULT 0,
  udfs_installed  int         NOT NULL DEFAULT 0,
  error_detail    text,
  syncer_version  text,
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT rls_sync_state_pkey        PRIMARY KEY (data_source_id),
  CONSTRAINT rls_sync_state_ds_fk       FOREIGN KEY (data_source_id) REFERENCES data_sources(id),
  CONSTRAINT rls_sync_state_status_ck   CHECK (sync_status IN ('pending','running','ok','error'))
);

-- ── 3. proxy_query_log ────────────────────────────────────────────────────────
-- Lightweight per-query attribution ring-buffer (last 7 days, partitioned by day).
-- Full audit lives in ClickHouse; this table powers the admin-console query inspector.
CREATE TABLE IF NOT EXISTS proxy_query_log (
  id                  uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id           uuid        NOT NULL,
  user_id             uuid        NOT NULL,
  data_source_id      uuid        NOT NULL,
  session_token_id    uuid,
  query_hash          text        NOT NULL,   -- sha256(normalized rewritten SQL)
  raw_sql_snippet     text,                   -- first 512 chars of original SQL
  rewritten_sql_snippet text,                 -- first 512 chars of rewritten SQL
  decision            text        NOT NULL,   -- 'allow' | 'deny' | 'error'
  denied_reason       text,
  masks_applied       text[],                 -- column names that were masked
  row_count           bigint,
  duration_ms         int,
  pdp_duration_ms     int,
  rewrite_duration_ms int,
  cost_gate_tripped   boolean     NOT NULL DEFAULT false,
  logged_at           timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT proxy_query_log_pkey       PRIMARY KEY (id, logged_at),
  CONSTRAINT proxy_query_log_decision_ck CHECK (decision IN ('allow','deny','error'))
) PARTITION BY RANGE (logged_at);

-- Create partitions for ±7 days rolling window (syncer creates future partitions).
DO $$
DECLARE
  day date;
BEGIN
  FOR day IN
    SELECT generate_series(
      current_date - interval '1 day',
      current_date + interval '7 days',
      interval '1 day'
    )::date
  LOOP
    EXECUTE format(
      'CREATE TABLE IF NOT EXISTS proxy_query_log_%s
         PARTITION OF proxy_query_log
         FOR VALUES FROM (%L) TO (%L)',
      to_char(day, 'YYYYMMDD'),
      day::timestamptz,
      (day + interval '1 day')::timestamptz
    );
  END LOOP;
END $$;

CREATE INDEX IF NOT EXISTS proxy_query_log_tenant_time_idx
  ON proxy_query_log (tenant_id, logged_at DESC);

CREATE INDEX IF NOT EXISTS proxy_query_log_user_idx
  ON proxy_query_log (user_id, logged_at DESC);

CREATE INDEX IF NOT EXISTS proxy_query_log_hash_idx
  ON proxy_query_log (query_hash, logged_at DESC);

-- ── 4. RLS ────────────────────────────────────────────────────────────────────
ALTER TABLE proxy_session_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE proxy_session_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY proxy_session_tokens_tenant_iso ON proxy_session_tokens
  USING (tenant_id::text = current_setting('app.tenant_id', true));

-- rls_sync_state has no tenant column (datasource key); admin-only access.
ALTER TABLE rls_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE rls_sync_state FORCE ROW LEVEL SECURITY;
CREATE POLICY rls_sync_state_admin_iso ON rls_sync_state
  USING (true);  -- app_admin may see all; enforced by role grant below

ALTER TABLE proxy_query_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE proxy_query_log FORCE ROW LEVEL SECURITY;
CREATE POLICY proxy_query_log_tenant_iso ON proxy_query_log
  USING (tenant_id::text = current_setting('app.tenant_id', true));

-- ── 5. Grants ─────────────────────────────────────────────────────────────────
GRANT SELECT                         ON proxy_session_tokens TO app_read;
GRANT SELECT, INSERT, UPDATE         ON proxy_session_tokens TO app_write;
GRANT SELECT, INSERT, UPDATE, DELETE ON proxy_session_tokens TO app_admin;
GRANT ALL PRIVILEGES                 ON proxy_session_tokens TO app_migrator;

GRANT SELECT, INSERT, UPDATE         ON rls_sync_state TO app_write;
GRANT SELECT, INSERT, UPDATE         ON rls_sync_state TO app_admin;
GRANT ALL PRIVILEGES                 ON rls_sync_state TO app_migrator;

GRANT SELECT                         ON proxy_query_log TO app_read;
GRANT SELECT, INSERT                 ON proxy_query_log TO app_write;
GRANT SELECT, INSERT, UPDATE         ON proxy_query_log TO app_admin;
GRANT ALL PRIVILEGES                 ON proxy_query_log TO app_migrator;

COMMIT;
