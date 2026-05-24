-- 0007_audit_tables.up.sql
-- policy_audit (append-only, hash-chained) and access_audit (partitioned by day).
BEGIN;

SET lock_timeout = '5s';

-- ── policy_audit ──────────────────────────────────────────────────────────────
-- Append-only; UPDATE and DELETE are revoked from all application roles.
-- actor_token is a deterministic HMAC-tokenized reference to actor_id; the raw
-- actor_id is stored for auditor queries but must not appear in downstream sinks
-- (Phase 5) to satisfy GDPR right-to-erasure requirements.
CREATE TABLE IF NOT EXISTS policy_audit (
  id            uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id     uuid        NOT NULL,
  actor_id      uuid        NOT NULL,
  actor_token   text        NOT NULL,
  action        text        NOT NULL,
  resource_type text        NOT NULL DEFAULT 'policy',
  resource_id   uuid,
  before        jsonb,
  after         jsonb,
  outcome       text        NOT NULL DEFAULT 'success',
  trace_id      text,
  -- Hash-chain fields populated by policy_audit_hash_chain trigger (migration 0010)
  prev_hash     bytea,
  row_hash      bytea,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT policy_audit_pkey        PRIMARY KEY (id),
  CONSTRAINT policy_audit_tenant_id_nn CHECK (tenant_id IS NOT NULL),
  CONSTRAINT policy_audit_outcome_ck   CHECK (outcome IN ('success','failure')),
  CONSTRAINT policy_audit_tenant_fk    FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

-- Revoke mutation rights from ALL roles; only DDL (migrator) can touch this table.
-- INSERT is retained so app roles can append; UPDATE/DELETE never permitted.
REVOKE UPDATE, DELETE ON policy_audit FROM PUBLIC;

GRANT SELECT, INSERT ON policy_audit TO app_write;
GRANT SELECT, INSERT ON policy_audit TO app_admin;
-- app_read can SELECT but NOT see before/after payload (SECURITY DEFINER view in Phase 4)
GRANT SELECT ON policy_audit TO app_read;
GRANT ALL PRIVILEGES ON policy_audit TO app_migrator;

-- ── access_audit ─────────────────────────────────────────────────────────────
-- Partitioned by day on created_at.  Foreign keys on tenant_id are kept as
-- non-FK indexed columns because PostgreSQL FK constraints on partitioned tables
-- have limitations on older managed DB versions.
CREATE TABLE IF NOT EXISTS access_audit (
  id             uuid             NOT NULL DEFAULT gen_uuidv7(),
  tenant_id      uuid             NOT NULL,
  user_id        uuid             NOT NULL,
  data_source_id uuid,
  resource       text             NOT NULL,
  action         text             NOT NULL,
  decision       text             NOT NULL,
  reason         text,
  row_count      integer,
  query_hash     text,
  duration_ms    integer,
  risk_score     double precision,
  metadata       jsonb            NOT NULL DEFAULT '{}',
  trace_id       text,
  created_at     timestamptz      NOT NULL DEFAULT now(),

  -- Partition key must be part of the primary key
  CONSTRAINT access_audit_pkey       PRIMARY KEY (id, created_at),
  CONSTRAINT access_audit_tenant_id_nn CHECK (tenant_id IS NOT NULL),
  CONSTRAINT access_audit_decision_ck  CHECK (decision IN ('allow','deny','mask'))
) PARTITION BY RANGE (created_at);

REVOKE UPDATE, DELETE ON access_audit FROM PUBLIC;

GRANT SELECT, INSERT ON access_audit TO app_write;
GRANT SELECT, INSERT ON access_audit TO app_admin;
GRANT SELECT         ON access_audit TO app_read;
GRANT ALL PRIVILEGES ON access_audit TO app_migrator;

COMMIT;
