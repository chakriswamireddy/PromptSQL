-- 0009_rls_policies.down.sql  (dev/staging only)
BEGIN;
-- Drop triggers
DROP TRIGGER IF EXISTS access_audit_tenant_check ON access_audit;
DROP TRIGGER IF EXISTS policy_audit_tenant_check ON policy_audit;

-- Drop RLS policies and disable enforcement
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'users','roles','user_roles','data_sources','data_classifications',
    'policies','policy_audit','access_audit','schema_metadata','doc_chunks'
  ] LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_iso ON %I', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
  END LOOP;
END
$$;
COMMIT;
