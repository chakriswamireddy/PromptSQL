-- 0001_extensions.up.sql
-- Install required PostgreSQL extensions. Pin pgvector to a known version to
-- prevent silent behaviour changes from an extension upgrade in managed DBs.
BEGIN;

SET lock_timeout = '5s';

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
-- Pin version so dev/staging/prod are identical.
CREATE EXTENSION IF NOT EXISTS vector VERSION '0.7.0';

-- pg_partman is optional: available in RDS/Aurora/Cloud SQL via parameter group.
-- If absent, the Go partition-cron job (apps/partition-cron) handles creation.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_partman') THEN
    EXECUTE 'CREATE EXTENSION IF NOT EXISTS pg_partman';
  END IF;
END
$$;

COMMIT;
