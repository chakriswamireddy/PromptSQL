-- 0009_rls_policies.up.sql
-- Enable and FORCE Row-Level Security on every tenant-scoped table.
-- FORCE means even superusers and table owners are subject to the policy;
-- this prevents accidental cross-tenant leaks from maintenance jobs.
--
-- Every policy uses current_setting('app.tenant_id', true) with the "missing_ok"
-- flag=true so a missing GUC returns NULL rather than raising an error.
-- A NULL context causes all comparisons to be false → zero rows returned (fail-closed).
BEGIN;

SET lock_timeout = '5s';

-- ── users ─────────────────────────────────────────────────────────────────────
ALTER TABLE users ENABLE  ROW LEVEL SECURITY;
ALTER TABLE users FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON users;
CREATE POLICY tenant_iso ON users
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── roles ─────────────────────────────────────────────────────────────────────
ALTER TABLE roles ENABLE  ROW LEVEL SECURITY;
ALTER TABLE roles FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON roles;
CREATE POLICY tenant_iso ON roles
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── user_roles ────────────────────────────────────────────────────────────────
ALTER TABLE user_roles ENABLE  ROW LEVEL SECURITY;
ALTER TABLE user_roles FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON user_roles;
CREATE POLICY tenant_iso ON user_roles
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── data_sources ──────────────────────────────────────────────────────────────
ALTER TABLE data_sources ENABLE  ROW LEVEL SECURITY;
ALTER TABLE data_sources FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON data_sources;
CREATE POLICY tenant_iso ON data_sources
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── data_classifications ──────────────────────────────────────────────────────
ALTER TABLE data_classifications ENABLE  ROW LEVEL SECURITY;
ALTER TABLE data_classifications FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON data_classifications;
CREATE POLICY tenant_iso ON data_classifications
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── policies ──────────────────────────────────────────────────────────────────
ALTER TABLE policies ENABLE  ROW LEVEL SECURITY;
ALTER TABLE policies FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON policies;
CREATE POLICY tenant_iso ON policies
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── policy_audit ──────────────────────────────────────────────────────────────
ALTER TABLE policy_audit ENABLE  ROW LEVEL SECURITY;
ALTER TABLE policy_audit FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON policy_audit;
CREATE POLICY tenant_iso ON policy_audit
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── access_audit ──────────────────────────────────────────────────────────────
ALTER TABLE access_audit ENABLE  ROW LEVEL SECURITY;
ALTER TABLE access_audit FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON access_audit;
CREATE POLICY tenant_iso ON access_audit
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── schema_metadata ───────────────────────────────────────────────────────────
ALTER TABLE schema_metadata ENABLE  ROW LEVEL SECURITY;
ALTER TABLE schema_metadata FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON schema_metadata;
CREATE POLICY tenant_iso ON schema_metadata
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── doc_chunks ────────────────────────────────────────────────────────────────
ALTER TABLE doc_chunks ENABLE  ROW LEVEL SECURITY;
ALTER TABLE doc_chunks FORCE   ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_iso ON doc_chunks;
CREATE POLICY tenant_iso ON doc_chunks
  USING      (tenant_id = current_setting('app.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── assert_tenant_id_match triggers on audit tables ──────────────────────────
-- Defence-in-depth: a BEFORE INSERT trigger rejects rows whose tenant_id
-- disagrees with the session GUC, even if RLS were somehow bypassed.
DROP TRIGGER IF EXISTS policy_audit_tenant_check ON policy_audit;
CREATE TRIGGER policy_audit_tenant_check
  BEFORE INSERT ON policy_audit
  FOR EACH ROW EXECUTE FUNCTION assert_tenant_id_match();

DROP TRIGGER IF EXISTS access_audit_tenant_check ON access_audit;
CREATE TRIGGER access_audit_tenant_check
  BEFORE INSERT ON access_audit
  FOR EACH ROW EXECUTE FUNCTION assert_tenant_id_match();

COMMIT;
