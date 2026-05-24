-- 0012_access_audit_partitions.up.sql
-- Pre-create daily partitions for access_audit covering today through +7 days.
-- The Go partition-cron job (created in Phase 1) extends this window on a
-- nightly schedule.  pg_partman, if available, replaces the cron entirely.
BEGIN;

SET lock_timeout = '5s';

-- Helper function: create a single day partition if it doesn't already exist.
CREATE OR REPLACE FUNCTION create_access_audit_partition(p_date date)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  part_name text;
  start_ts  timestamptz;
  end_ts    timestamptz;
BEGIN
  part_name := 'access_audit_' || to_char(p_date, 'YYYY_MM_DD');
  start_ts  := p_date::timestamptz AT TIME ZONE 'UTC';
  end_ts    := (p_date + 1)::timestamptz AT TIME ZONE 'UTC';

  IF NOT EXISTS (
    SELECT 1 FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relname = part_name AND n.nspname = 'public'
  ) THEN
    EXECUTE format(
      'CREATE TABLE %I PARTITION OF access_audit
         FOR VALUES FROM (%L) TO (%L)',
      part_name, start_ts, end_ts
    );
  END IF;
END
$$;

-- Helper function: idempotent index creation on a specific partition.
CREATE OR REPLACE FUNCTION index_access_audit_partition(p_date date)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  part_name text := 'access_audit_' || to_char(p_date, 'YYYY_MM_DD');
  idx_name  text := 'idx_' || part_name || '_tenant_user';
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = idx_name) THEN
    EXECUTE format(
      'CREATE INDEX %I ON %I (tenant_id, user_id, created_at DESC)',
      idx_name, part_name
    );
  END IF;
END
$$;

-- Pre-create partitions: today and the next 7 days
DO $$
DECLARE
  i integer;
  d date;
BEGIN
  FOR i IN 0..7 LOOP
    d := (current_date + i);
    PERFORM create_access_audit_partition(d);
    PERFORM index_access_audit_partition(d);
  END LOOP;
END
$$;

-- If pg_partman is available, hand off maintenance to it.
-- Otherwise the partition-cron app handles nightly pre-creation.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_partman') THEN
    PERFORM partman.create_parent(
      p_parent_table  => 'public.access_audit',
      p_control       => 'created_at',
      p_interval      => '1 day',
      p_premake       => 7
    );
  END IF;
END
$$;

GRANT EXECUTE ON FUNCTION create_access_audit_partition(date)  TO app_admin;
GRANT EXECUTE ON FUNCTION index_access_audit_partition(date)   TO app_admin;

COMMIT;
