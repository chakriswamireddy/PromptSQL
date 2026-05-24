-- 0004_core_tables.down.sql  (dev/staging only)
BEGIN;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
COMMIT;
