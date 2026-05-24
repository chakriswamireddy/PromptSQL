-- 0013_auth_tables.down.sql
-- Reverses Phase 2 auth tables. Dev/staging only; never run in production.
BEGIN;
DROP TABLE IF EXISTS service_accounts CASCADE;
DROP TABLE IF EXISTS api_keys         CASCADE;
DROP TABLE IF EXISTS mfa_credentials  CASCADE;
DROP TABLE IF EXISTS refresh_tokens   CASCADE;
ALTER TABLE users
  DROP COLUMN IF EXISTS password_hash,
  DROP COLUMN IF EXISTS failed_login_attempts,
  DROP COLUMN IF EXISTS locked_until;
COMMIT;
