-- 0004_core_tables.up.sql
-- tenants, users, roles, user_roles
BEGIN;

SET lock_timeout = '5s';

-- ── tenants ──────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tenants (
  id               uuid        NOT NULL DEFAULT gen_uuidv7(),
  slug             citext      NOT NULL,
  display_name     text        NOT NULL,
  plan_tier        text        NOT NULL DEFAULT 'free',
  data_residency   text        NOT NULL DEFAULT 'us-east-1',
  compliance_modes jsonb       NOT NULL DEFAULT '[]',
  status           text        NOT NULL DEFAULT 'active',
  deleted_at       timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tenants_pkey             PRIMARY KEY (id),
  CONSTRAINT tenants_id_nn            CHECK (id IS NOT NULL),
  CONSTRAINT tenants_slug_uq          UNIQUE (slug),
  CONSTRAINT tenants_plan_tier_ck     CHECK (plan_tier IN ('free','starter','business','enterprise')),
  CONSTRAINT tenants_status_ck        CHECK (status IN ('active','suspended','deleted')),
  CONSTRAINT tenants_compliance_arr_ck CHECK (jsonb_typeof(compliance_modes) = 'array')
);

CREATE TRIGGER tenants_set_updated_at
  BEFORE UPDATE ON tenants
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── users ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
  id                     uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id              uuid        NOT NULL,
  email                  citext      NOT NULL,
  external_idp_subject   text,
  status                 text        NOT NULL DEFAULT 'active',
  attributes             jsonb       NOT NULL DEFAULT '{}',
  -- NULL means "never invalidated"; Phase 2 sets this on logout / key rotation
  session_invalidated_at timestamptz,
  deleted_at             timestamptz,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT users_pkey            PRIMARY KEY (id),
  CONSTRAINT users_tenant_id_nn    CHECK (tenant_id IS NOT NULL),
  CONSTRAINT users_tenant_email_uq UNIQUE (tenant_id, email),
  CONSTRAINT users_status_ck       CHECK (status IN ('active','suspended','deleted')),
  CONSTRAINT users_attrs_obj_ck    CHECK (jsonb_typeof(attributes) IN ('object','null')),
  CONSTRAINT users_tenant_id_fk    FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE TRIGGER users_set_updated_at
  BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── roles ─────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS roles (
  id             uuid    NOT NULL DEFAULT gen_uuidv7(),
  tenant_id      uuid    NOT NULL,
  name           text    NOT NULL,
  description    text    NOT NULL DEFAULT '',
  parent_role_id uuid,
  is_system      boolean NOT NULL DEFAULT false,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT roles_pkey           PRIMARY KEY (id),
  CONSTRAINT roles_tenant_id_nn   CHECK (tenant_id IS NOT NULL),
  CONSTRAINT roles_tenant_name_uq UNIQUE (tenant_id, name),
  CONSTRAINT roles_tenant_id_fk   FOREIGN KEY (tenant_id)      REFERENCES tenants(id),
  CONSTRAINT roles_parent_fk      FOREIGN KEY (parent_role_id) REFERENCES roles(id)
);

CREATE TRIGGER roles_set_updated_at
  BEFORE UPDATE ON roles
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── user_roles ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS user_roles (
  user_id    uuid        NOT NULL,
  role_id    uuid        NOT NULL,
  tenant_id  uuid        NOT NULL,
  granted_by uuid        NOT NULL,
  granted_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz,

  CONSTRAINT user_roles_pkey          PRIMARY KEY (user_id, role_id),
  CONSTRAINT user_roles_tenant_id_nn  CHECK (tenant_id IS NOT NULL),
  CONSTRAINT user_roles_user_fk       FOREIGN KEY (user_id)    REFERENCES users(id),
  CONSTRAINT user_roles_role_fk       FOREIGN KEY (role_id)    REFERENCES roles(id),
  CONSTRAINT user_roles_granted_by_fk FOREIGN KEY (granted_by) REFERENCES users(id)
);

-- ── per-table grants ──────────────────────────────────────────────────────────
GRANT SELECT                   ON tenants    TO app_read;
GRANT SELECT, INSERT, UPDATE   ON tenants    TO app_admin;

GRANT SELECT                   ON users      TO app_read;
GRANT SELECT, INSERT, UPDATE   ON users      TO app_write;
GRANT SELECT, INSERT, UPDATE   ON users      TO app_admin;

GRANT SELECT                   ON roles      TO app_read;
GRANT SELECT, INSERT, UPDATE   ON roles      TO app_admin;

GRANT SELECT                   ON user_roles TO app_read;
GRANT SELECT, INSERT, DELETE   ON user_roles TO app_admin;

-- migrator owns DDL; no runtime SELECT against data tables in prod sessions
GRANT ALL PRIVILEGES ON tenants, users, roles, user_roles TO app_migrator;

COMMIT;
