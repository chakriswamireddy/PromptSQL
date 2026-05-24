-- 0013_auth_tables.up.sql
-- Phase 2: auth tables — refresh_tokens, mfa_credentials, api_keys (reserved),
-- service_accounts (reserved). Extends users with password_hash + lockout columns.
BEGIN;

SET lock_timeout = '5s';

-- ── extend users ──────────────────────────────────────────────────────────────
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS password_hash         text,
  ADD COLUMN IF NOT EXISTS failed_login_attempts integer     NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS locked_until          timestamptz;

CREATE INDEX IF NOT EXISTS users_locked_until_idx ON users (tenant_id, locked_until)
  WHERE locked_until IS NOT NULL;

-- ── refresh_tokens ────────────────────────────────────────────────────────────
-- Opaque tokens; only the SHA-256 hex digest is stored.
-- Rotation: every refresh issues a new token and sets prev_token_id.
-- Reuse of a rotated-away token (non-null prev_token_id still valid) = theft signal.
CREATE TABLE IF NOT EXISTS refresh_tokens (
  id            uuid        NOT NULL DEFAULT gen_uuidv7(),
  user_id       uuid        NOT NULL,
  tenant_id     uuid        NOT NULL,
  token_hash    text        NOT NULL,
  session_id    uuid        NOT NULL,
  prev_token_id uuid,
  expires_at    timestamptz NOT NULL,
  revoked_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT refresh_tokens_pkey      PRIMARY KEY (id),
  CONSTRAINT refresh_tokens_hash_uq   UNIQUE (token_hash),
  CONSTRAINT refresh_tokens_user_fk   FOREIGN KEY (user_id)   REFERENCES users(id),
  CONSTRAINT refresh_tokens_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE INDEX IF NOT EXISTS refresh_tokens_user_idx
  ON refresh_tokens (user_id, tenant_id);
CREATE INDEX IF NOT EXISTS refresh_tokens_expire_idx
  ON refresh_tokens (expires_at) WHERE revoked_at IS NULL;

-- ── mfa_credentials ───────────────────────────────────────────────────────────
-- kind='totp'   → secret_enc is an AES-GCM encrypted TOTP secret (base64).
-- kind='backup' → secret_enc is an Argon2id hash of the single-use backup code.
--                 used_at is set on consumption; non-null = consumed.
CREATE TABLE IF NOT EXISTS mfa_credentials (
  id          uuid        NOT NULL DEFAULT gen_uuidv7(),
  user_id     uuid        NOT NULL,
  tenant_id   uuid        NOT NULL,
  kind        text        NOT NULL DEFAULT 'totp',
  secret_enc  text        NOT NULL,
  is_verified boolean     NOT NULL DEFAULT false,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT mfa_credentials_pkey      PRIMARY KEY (id),
  CONSTRAINT mfa_credentials_kind_ck   CHECK (kind IN ('totp','backup')),
  CONSTRAINT mfa_credentials_user_fk   FOREIGN KEY (user_id)   REFERENCES users(id),
  CONSTRAINT mfa_credentials_tenant_fk FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE INDEX IF NOT EXISTS mfa_credentials_user_kind_idx
  ON mfa_credentials (user_id, kind);
-- Enforce single active TOTP per user
CREATE UNIQUE INDEX IF NOT EXISTS mfa_credentials_one_totp_per_user
  ON mfa_credentials (user_id) WHERE kind = 'totp' AND is_verified = true;

-- ── api_keys (reserved for Phase 2.5) ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS api_keys (
  id            uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id     uuid        NOT NULL,
  owner_user_id uuid        NOT NULL,
  name          text        NOT NULL,
  key_hash      text        NOT NULL,
  scopes        text[]      NOT NULL DEFAULT '{}',
  expires_at    timestamptz,
  last_used_at  timestamptz,
  revoked_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT api_keys_pkey      PRIMARY KEY (id),
  CONSTRAINT api_keys_hash_uq   UNIQUE (key_hash),
  CONSTRAINT api_keys_tenant_fk FOREIGN KEY (tenant_id)     REFERENCES tenants(id),
  CONSTRAINT api_keys_owner_fk  FOREIGN KEY (owner_user_id) REFERENCES users(id)
);

-- ── service_accounts (reserved for Phase 2.5) ────────────────────────────────
CREATE TABLE IF NOT EXISTS service_accounts (
  id               uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id        uuid        NOT NULL,
  name             text        NOT NULL,
  default_role     text        NOT NULL DEFAULT 'analyst',
  cert_fingerprint text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT service_accounts_pkey           PRIMARY KEY (id),
  CONSTRAINT service_accounts_tenant_name_uq UNIQUE (tenant_id, name),
  CONSTRAINT service_accounts_tenant_fk      FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

-- ── grants ────────────────────────────────────────────────────────────────────
GRANT SELECT, INSERT, UPDATE ON refresh_tokens   TO app_write;
GRANT SELECT, INSERT, UPDATE ON refresh_tokens   TO app_admin;
GRANT SELECT                 ON refresh_tokens   TO app_read;
GRANT ALL PRIVILEGES         ON refresh_tokens   TO app_migrator;

GRANT SELECT, INSERT, UPDATE ON mfa_credentials  TO app_write;
GRANT SELECT, INSERT, UPDATE ON mfa_credentials  TO app_admin;
GRANT SELECT                 ON mfa_credentials  TO app_read;
GRANT ALL PRIVILEGES         ON mfa_credentials  TO app_migrator;

GRANT SELECT, INSERT, UPDATE ON api_keys         TO app_admin;
GRANT SELECT                 ON api_keys         TO app_read;
GRANT ALL PRIVILEGES         ON api_keys         TO app_migrator;

GRANT SELECT, INSERT, UPDATE ON service_accounts TO app_admin;
GRANT SELECT                 ON service_accounts TO app_read;
GRANT ALL PRIVILEGES         ON service_accounts TO app_migrator;

-- ── RLS on new tables ─────────────────────────────────────────────────────────
ALTER TABLE refresh_tokens   ENABLE ROW LEVEL SECURITY;
ALTER TABLE refresh_tokens   FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON refresh_tokens;
CREATE POLICY tenant_iso ON refresh_tokens
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE mfa_credentials  ENABLE ROW LEVEL SECURITY;
ALTER TABLE mfa_credentials  FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON mfa_credentials;
CREATE POLICY tenant_iso ON mfa_credentials
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE api_keys         ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys         FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON api_keys;
CREATE POLICY tenant_iso ON api_keys
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE service_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE service_accounts FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON service_accounts;
CREATE POLICY tenant_iso ON service_accounts
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

COMMIT;
