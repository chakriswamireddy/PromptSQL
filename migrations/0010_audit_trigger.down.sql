-- 0010_audit_trigger.down.sql  (dev/staging only)
BEGIN;
DROP FUNCTION IF EXISTS verify_policy_audit_chain(uuid, timestamptz);
DROP TRIGGER  IF EXISTS policy_audit_hash_trigger ON policy_audit;
DROP FUNCTION IF EXISTS policy_audit_hash_chain();
COMMIT;
