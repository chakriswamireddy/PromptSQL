-- 0002_roles_grants.down.sql  (dev/staging only)
BEGIN;
REVOKE app_read, app_write, app_admin FROM app_login_user;
REVOKE app_migrator FROM app_migration_login;
DROP ROLE IF EXISTS app_break_glass;
DROP ROLE IF EXISTS app_migrator;
DROP ROLE IF EXISTS app_admin;
DROP ROLE IF EXISTS app_write;
DROP ROLE IF EXISTS app_read;
COMMIT;
