-- 0015_admin_console_tables.down.sql
BEGIN;

DROP TABLE IF EXISTS policy_diff_reports CASCADE;
DROP TABLE IF EXISTS personas CASCADE;
DROP TABLE IF EXISTS outbox_events CASCADE;

ALTER TABLE policies DROP COLUMN IF EXISTS etag;
ALTER TABLE policies DROP COLUMN IF EXISTS submitted_at;
ALTER TABLE policies DROP COLUMN IF EXISTS submitted_by;
ALTER TABLE policies DROP COLUMN IF EXISTS column_masks;

ALTER TABLE policies DROP CONSTRAINT IF EXISTS policies_status_ck;
ALTER TABLE policies ADD CONSTRAINT policies_status_ck
  CHECK (status IN ('draft','active','archived'));

COMMIT;
