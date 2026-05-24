-- 0012_access_audit_partitions.down.sql  (dev/staging only)
BEGIN;
DROP FUNCTION IF EXISTS index_access_audit_partition(date);
DROP FUNCTION IF EXISTS create_access_audit_partition(date);
-- Child partitions are dropped automatically when the parent is dropped (migration 0007 down)
COMMIT;
