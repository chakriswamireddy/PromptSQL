-- 0002_roles_grants.up.sql
-- Create scoped database roles and assign least-privilege grants.
-- app_login_user and app_migration_login already exist from the container
-- init script (infra/postgres/init/01-roles.sql); only supplementary roles
-- are created here so this migration is idempotent on a fresh OR existing DB.
BEGIN;

SET lock_timeout = '5s';

DO $$
BEGIN
  -- Non-login functional roles
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_read') THEN
    CREATE ROLE app_read NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_write') THEN
    CREATE ROLE app_write NOINHERIT;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_admin') THEN
    CREATE ROLE app_admin NOINHERIT;
  END IF;
  -- app_migrator bypasses RLS; only assumable by app_migration_login
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_migrator') THEN
    CREATE ROLE app_migrator NOINHERIT BYPASSRLS;
  END IF;
  -- app_break_glass bypasses RLS; only assumable inside an audited session (Phase 14)
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_break_glass') THEN
    CREATE ROLE app_break_glass NOINHERIT BYPASSRLS;
  END IF;
END
$$;

-- Wire functional roles into login roles
GRANT app_read, app_write, app_admin TO app_login_user;
-- break_glass is NOT pre-granted; Phase 14 grants it transiently in audited sessions
GRANT app_migrator TO app_migration_login;

COMMIT;
