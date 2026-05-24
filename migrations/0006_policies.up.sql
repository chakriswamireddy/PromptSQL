-- 0006_policies.up.sql
-- policies table with versioning strategy: (tenant_id, name, version) unique;
-- old versions are never deleted, only archived.
BEGIN;

SET lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS policies (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  name            text        NOT NULL,
  -- Monotonic per (tenant_id, name); managed by app, never auto-incremented
  version         integer     NOT NULL DEFAULT 1,
  status          text        NOT NULL DEFAULT 'draft',
  effect          text        NOT NULL DEFAULT 'allow',
  subject_match   jsonb       NOT NULL DEFAULT '{}',
  resource_match  jsonb       NOT NULL DEFAULT '{}',
  action          text        NOT NULL DEFAULT '*',
  -- Validated by Phase 3 DSL parser; CHECK enforces basic type safety only
  conditions      jsonb,
  obligations     jsonb,
  allowed_columns text[],
  denied_columns  text[],
  row_filter      jsonb,
  created_by      uuid        NOT NULL,
  approved_by     uuid,
  effective_from  timestamptz,
  effective_to    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT policies_pkey              PRIMARY KEY (id),
  CONSTRAINT policies_tenant_id_nn      CHECK (tenant_id IS NOT NULL),
  CONSTRAINT policies_tenant_name_ver_uq UNIQUE (tenant_id, name, version),
  CONSTRAINT policies_status_ck         CHECK (status IN ('draft','active','archived')),
  CONSTRAINT policies_effect_ck         CHECK (effect IN ('allow','deny')),
  -- Cheap type guard; full semantic validation belongs to the Phase 3 DSL
  CONSTRAINT policies_conditions_ck     CHECK (conditions  IS NULL OR jsonb_typeof(conditions)  IN ('object','null')),
  CONSTRAINT policies_obligations_ck    CHECK (obligations IS NULL OR jsonb_typeof(obligations) IN ('object','array','null')),
  CONSTRAINT policies_row_filter_ck     CHECK (row_filter  IS NULL OR jsonb_typeof(row_filter)  IN ('object','null')),
  CONSTRAINT policies_subject_obj_ck    CHECK (jsonb_typeof(subject_match)  IN ('object','null')),
  CONSTRAINT policies_resource_obj_ck   CHECK (jsonb_typeof(resource_match) IN ('object','null')),
  CONSTRAINT policies_tenant_fk         FOREIGN KEY (tenant_id)   REFERENCES tenants(id),
  CONSTRAINT policies_created_by_fk     FOREIGN KEY (created_by)  REFERENCES users(id),
  CONSTRAINT policies_approved_by_fk    FOREIGN KEY (approved_by) REFERENCES users(id)
);

CREATE TRIGGER policies_set_updated_at
  BEFORE UPDATE ON policies
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── per-table grants ──────────────────────────────────────────────────────────
GRANT SELECT                 ON policies TO app_read;
GRANT SELECT, INSERT         ON policies TO app_write;
-- UPDATE is allowed so drafts can be edited; transitions to 'active' require app_admin
GRANT SELECT, INSERT, UPDATE ON policies TO app_admin;

GRANT ALL PRIVILEGES ON policies TO app_migrator;

COMMIT;
