-- 0015_admin_console_tables.up.sql
-- Phase 4: Admin Console & Simulator tables.
-- Adds:
--   • pending_review to policies.status
--   • column_masks to policies
--   • outbox_events  — transactional outbox for pub/sub relay
--   • personas        — saved simulator subjects (tenant-scoped)
--   • policy_diff_reports — cached diff results keyed by (draft_hash, active_hash)
BEGIN;

SET lock_timeout = '5s';

-- ── 1. Extend policies.status to include pending_review ──────────────────────
ALTER TABLE policies DROP CONSTRAINT IF EXISTS policies_status_ck;
ALTER TABLE policies ADD CONSTRAINT policies_status_ck
  CHECK (status IN ('draft','pending_review','active','archived'));

-- column_masks: {"column_name": "mask_fn_name"}, applied after allow-list.
ALTER TABLE policies ADD COLUMN IF NOT EXISTS
  column_masks jsonb
  CONSTRAINT policies_column_masks_ck CHECK (column_masks IS NULL OR jsonb_typeof(column_masks) = 'object');

-- submitted_by / submitted_at for review trail.
ALTER TABLE policies ADD COLUMN IF NOT EXISTS submitted_by  uuid REFERENCES users(id);
ALTER TABLE policies ADD COLUMN IF NOT EXISTS submitted_at  timestamptz;
ALTER TABLE policies ADD COLUMN IF NOT EXISTS etag          text;

-- ── 2. outbox_events ─────────────────────────────────────────────────────────
-- Transactional outbox: writes happen in the same transaction as the policy
-- mutation; a relay process publishes to Redis pub/sub and sets sent_at.
CREATE TABLE IF NOT EXISTS outbox_events (
  id          uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id   uuid        NOT NULL,
  kind        text        NOT NULL,   -- e.g. 'policy.activated', 'policy.archived'
  payload     jsonb       NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  sent_at     timestamptz,            -- null = pending delivery

  CONSTRAINT outbox_events_pkey      PRIMARY KEY (id),
  CONSTRAINT outbox_events_kind_ck   CHECK (kind ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
  CONSTRAINT outbox_events_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE INDEX IF NOT EXISTS outbox_events_pending_idx
  ON outbox_events (created_at)
  WHERE sent_at IS NULL;

-- ── 3. personas ──────────────────────────────────────────────────────────────
-- Saved simulator subjects (synthetic SessionContext attributes).
CREATE TABLE IF NOT EXISTS personas (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  name            text        NOT NULL,
  description     text,
  -- Full SessionContext attributes blob; validated by app.
  attributes      jsonb       NOT NULL DEFAULT '{}',
  owner_user_id   uuid        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT personas_pkey        PRIMARY KEY (id),
  CONSTRAINT personas_name_uq     UNIQUE (tenant_id, name),
  CONSTRAINT personas_tenant_fk   FOREIGN KEY (tenant_id)     REFERENCES tenants(id),
  CONSTRAINT personas_owner_fk    FOREIGN KEY (owner_user_id) REFERENCES users(id),
  CONSTRAINT personas_attrs_ck    CHECK (jsonb_typeof(attributes) = 'object')
);

CREATE TRIGGER personas_set_updated_at
  BEFORE UPDATE ON personas
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── 4. policy_diff_reports ───────────────────────────────────────────────────
-- Cached diff results so multiple reviewers see the same report for the same
-- (draft_hash, active_set_hash, sample_size) triple.
CREATE TABLE IF NOT EXISTS policy_diff_reports (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  draft_hash      text        NOT NULL,   -- sha256 of draft policy JSON
  active_hash     text        NOT NULL,   -- sha256 of active policy-set JSON
  sample_size     integer     NOT NULL DEFAULT 20,
  body            jsonb       NOT NULL,   -- structured diff result
  created_by      uuid        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),

  CONSTRAINT policy_diff_reports_pkey      PRIMARY KEY (id),
  CONSTRAINT policy_diff_reports_uq        UNIQUE (tenant_id, draft_hash, active_hash, sample_size),
  CONSTRAINT policy_diff_reports_tenant_fk FOREIGN KEY (tenant_id)   REFERENCES tenants(id),
  CONSTRAINT policy_diff_reports_user_fk   FOREIGN KEY (created_by)  REFERENCES users(id),
  CONSTRAINT policy_diff_reports_body_ck   CHECK (jsonb_typeof(body) = 'object')
);

CREATE INDEX IF NOT EXISTS policy_diff_reports_tenant_idx
  ON policy_diff_reports (tenant_id, created_at DESC);

-- ── 5. RLS ───────────────────────────────────────────────────────────────────
ALTER TABLE outbox_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox_events FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_events_tenant_iso ON outbox_events
  USING (tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE personas ENABLE ROW LEVEL SECURITY;
ALTER TABLE personas FORCE ROW LEVEL SECURITY;
CREATE POLICY personas_tenant_iso ON personas
  USING (tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE policy_diff_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_diff_reports FORCE ROW LEVEL SECURITY;
CREATE POLICY policy_diff_reports_tenant_iso ON policy_diff_reports
  USING (tenant_id::text = current_setting('app.tenant_id', true));

-- ── 6. Grants ─────────────────────────────────────────────────────────────────
GRANT SELECT                           ON outbox_events       TO app_read;
GRANT SELECT, INSERT, UPDATE           ON outbox_events       TO app_write;
GRANT SELECT, INSERT, UPDATE           ON outbox_events       TO app_admin;
GRANT ALL PRIVILEGES                   ON outbox_events       TO app_migrator;

GRANT SELECT                           ON personas            TO app_read;
GRANT SELECT, INSERT, UPDATE, DELETE   ON personas            TO app_write;
GRANT SELECT, INSERT, UPDATE, DELETE   ON personas            TO app_admin;
GRANT ALL PRIVILEGES                   ON personas            TO app_migrator;

GRANT SELECT                           ON policy_diff_reports TO app_read;
GRANT SELECT, INSERT                   ON policy_diff_reports TO app_write;
GRANT SELECT, INSERT                   ON policy_diff_reports TO app_admin;
GRANT ALL PRIVILEGES                   ON policy_diff_reports TO app_migrator;

COMMIT;
