-- Phase 0: reserve Postgres roles so Phase 1 only adds schema, never roles.
-- Run via docker-entrypoint-initdb.d on first container start.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_migration_login') THEN
    CREATE ROLE app_migration_login LOGIN PASSWORD 'changeme_migration';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_login_user') THEN
    CREATE ROLE app_login_user LOGIN PASSWORD 'changeme_app';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_readonly') THEN
    CREATE ROLE app_readonly;
  END IF;
END
$$;

GRANT CONNECT ON DATABASE governance TO app_login_user;
GRANT CONNECT ON DATABASE governance TO app_migration_login;
