-- 0007_audit_tables.down.sql  (dev/staging only)
BEGIN;
DROP TABLE IF EXISTS access_audit;
DROP TABLE IF EXISTS policy_audit;
COMMIT;
