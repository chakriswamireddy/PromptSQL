-- 0014_policy_set_versions.up.sql
-- Adds policy_set_versions for cache-key versioning in the PDP.
-- A row is written (or updated) whenever any policy for a tenant is mutated.
-- The PDP reads max(version) per tenant to detect staleness without scanning
-- the full policies table.
BEGIN;

SET lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS policy_set_versions (
  tenant_id   uuid        NOT NULL,
  version     bigint      NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT policy_set_versions_pkey    PRIMARY KEY (tenant_id),
  CONSTRAINT policy_set_versions_tenant  FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

-- Bump version and updated_at atomically on any policies mutation.
CREATE OR REPLACE FUNCTION bump_policy_set_version()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO policy_set_versions (tenant_id, version, updated_at)
    VALUES (COALESCE(NEW.tenant_id, OLD.tenant_id), 1, now())
  ON CONFLICT (tenant_id) DO UPDATE
    SET version    = policy_set_versions.version + 1,
        updated_at = now();
  RETURN NEW;
END;
$$;

CREATE TRIGGER policies_bump_version
  AFTER INSERT OR UPDATE OR DELETE ON policies
  FOR EACH ROW EXECUTE FUNCTION bump_policy_set_version();

-- ── RLS ───────────────────────────────────────────────────────────────────────
ALTER TABLE policy_set_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_set_versions FORCE ROW LEVEL SECURITY;

CREATE POLICY policy_set_versions_tenant_isolation ON policy_set_versions
  USING (tenant_id::text = current_setting('app.tenant_id', true));

-- ── grants ────────────────────────────────────────────────────────────────────
GRANT SELECT                 ON policy_set_versions TO app_read;
GRANT SELECT, INSERT, UPDATE ON policy_set_versions TO app_write;
GRANT SELECT, INSERT, UPDATE ON policy_set_versions TO app_admin;
GRANT ALL PRIVILEGES         ON policy_set_versions TO app_migrator;

-- ── index ─────────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS policy_set_versions_updated_idx
  ON policy_set_versions (tenant_id, updated_at DESC);

COMMIT;
