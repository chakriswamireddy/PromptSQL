-- 0003_utility_functions.down.sql  (dev/staging only)
BEGIN;
DROP FUNCTION IF EXISTS assert_tenant_id_match();
DROP FUNCTION IF EXISTS set_updated_at();
DROP FUNCTION IF EXISTS gen_uuidv7();
COMMIT;
