-- 0018_schema_catalog.up.sql
-- Phase 7: Extend schema_metadata; add crawl_runs, inferred_relationships, embedding_queue.
BEGIN;

SET lock_timeout = '5s';

-- ── Extend schema_metadata with Phase 7 crawler columns ───────────────────────
-- RLS FORCE on schema_metadata was enabled in migration 0009; no duplicate needed.
ALTER TABLE schema_metadata
  ADD COLUMN IF NOT EXISTS sample_values        text[]      NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS embedding_model      text,
  ADD COLUMN IF NOT EXISTS embedding_dimensions int,
  ADD COLUMN IF NOT EXISTS classified_by        text        NOT NULL DEFAULT 'steward',
  ADD COLUMN IF NOT EXISTS column_position      int,
  ADD COLUMN IF NOT EXISTS column_default       text,
  ADD COLUMN IF NOT EXISTS table_comment        text,
  ADD COLUMN IF NOT EXISTS column_comment       text,
  ADD COLUMN IF NOT EXISTS fk_references        jsonb       NOT NULL DEFAULT '[]',
  ADD COLUMN IF NOT EXISTS index_names          text[]      NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS last_crawled_at      timestamptz,
  ADD COLUMN IF NOT EXISTS dropped_at           timestamptz;

ALTER TABLE schema_metadata
  ADD CONSTRAINT schema_metadata_classified_by_ck
    CHECK (classified_by IN ('pattern', 'steward', 'ml'));

-- ── Extend data_classifications with classification provenance ─────────────────
-- RLS FORCE on data_classifications was enabled in migration 0009; no duplicate needed.
ALTER TABLE data_classifications
  ADD COLUMN IF NOT EXISTS classified_by  text DEFAULT 'steward',
  ADD COLUMN IF NOT EXISTS pattern_id     uuid;

ALTER TABLE data_classifications
  ADD CONSTRAINT data_classifications_classified_by_ck
    CHECK (classified_by IN ('pattern', 'steward', 'ml'));

-- ── crawl_runs ─────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS crawl_runs (
  id              uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid        NOT NULL,
  data_source_id  uuid        NOT NULL,
  status          text        NOT NULL DEFAULT 'running',
  triggered_by    text        NOT NULL DEFAULT 'scheduler',
  columns_new     int         NOT NULL DEFAULT 0,
  columns_changed int         NOT NULL DEFAULT 0,
  columns_dropped int         NOT NULL DEFAULT 0,
  error_message   text,
  started_at      timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT crawl_runs_pkey           PRIMARY KEY (id),
  CONSTRAINT crawl_runs_tenant_id_nn   CHECK (tenant_id IS NOT NULL),
  CONSTRAINT crawl_runs_status_ck      CHECK (status IN ('running', 'success', 'failed')),
  CONSTRAINT crawl_runs_trigger_ck     CHECK (triggered_by IN ('scheduler', 'admin', 'api')),
  CONSTRAINT crawl_runs_tenant_fk      FOREIGN KEY (tenant_id)       REFERENCES tenants(id),
  CONSTRAINT crawl_runs_source_fk      FOREIGN KEY (data_source_id)  REFERENCES data_sources(id)
);

CREATE TRIGGER crawl_runs_set_updated_at
  BEFORE UPDATE ON crawl_runs
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE crawl_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE crawl_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY crawl_runs_tenant_isolation ON crawl_runs
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── inferred_relationships ─────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS inferred_relationships (
  id              uuid         NOT NULL DEFAULT gen_uuidv7(),
  tenant_id       uuid         NOT NULL,
  data_source_id  uuid         NOT NULL,
  from_schema     text         NOT NULL,
  from_table      text         NOT NULL,
  from_column     text         NOT NULL,
  to_schema       text         NOT NULL,
  to_table        text         NOT NULL,
  to_column       text         NOT NULL,
  confidence      numeric(5,4) NOT NULL DEFAULT 1.0,
  source          text         NOT NULL DEFAULT 'fk',
  created_at      timestamptz  NOT NULL DEFAULT now(),
  updated_at      timestamptz  NOT NULL DEFAULT now(),

  CONSTRAINT inferred_relationships_pkey       PRIMARY KEY (id),
  CONSTRAINT inferred_relationships_tid_nn     CHECK (tenant_id IS NOT NULL),
  CONSTRAINT inferred_relationships_source_ck  CHECK (source IN ('fk', 'query', 'ml')),
  CONSTRAINT inferred_relationships_unique     UNIQUE (
    tenant_id, data_source_id,
    from_schema, from_table, from_column,
    to_schema,   to_table,   to_column
  ),
  CONSTRAINT inferred_relationships_tenant_fk  FOREIGN KEY (tenant_id)      REFERENCES tenants(id),
  CONSTRAINT inferred_relationships_ds_fk      FOREIGN KEY (data_source_id) REFERENCES data_sources(id)
);

CREATE TRIGGER inferred_relationships_set_updated_at
  BEFORE UPDATE ON inferred_relationships
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE inferred_relationships ENABLE ROW LEVEL SECURITY;
ALTER TABLE inferred_relationships FORCE ROW LEVEL SECURITY;

CREATE POLICY inferred_relationships_tenant_isolation ON inferred_relationships
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── embedding_queue ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS embedding_queue (
  id            uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id     uuid        NOT NULL,
  column_id     uuid        NOT NULL,
  payload_hash  text        NOT NULL,
  model         text        NOT NULL DEFAULT 'text-embedding-3-small',
  dimensions    int         NOT NULL DEFAULT 1536,
  status        text        NOT NULL DEFAULT 'pending',
  attempts      int         NOT NULL DEFAULT 0,
  last_error    text,
  enqueued_at   timestamptz NOT NULL DEFAULT now(),
  processed_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT embedding_queue_pkey        PRIMARY KEY (id),
  CONSTRAINT embedding_queue_tid_nn      CHECK (tenant_id IS NOT NULL),
  CONSTRAINT embedding_queue_status_ck   CHECK (status IN ('pending', 'processing', 'done', 'failed')),
  CONSTRAINT embedding_queue_unique      UNIQUE (column_id, model, payload_hash),
  CONSTRAINT embedding_queue_tenant_fk   FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT embedding_queue_column_fk   FOREIGN KEY (column_id) REFERENCES schema_metadata(id)
);

CREATE TRIGGER embedding_queue_set_updated_at
  BEFORE UPDATE ON embedding_queue
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE embedding_queue ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_queue FORCE ROW LEVEL SECURITY;

CREATE POLICY embedding_queue_tenant_isolation ON embedding_queue
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── Indexes ────────────────────────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS crawl_runs_tenant_ds_idx
  ON crawl_runs (tenant_id, data_source_id, started_at DESC);

CREATE INDEX IF NOT EXISTS inferred_relationships_tenant_idx
  ON inferred_relationships (tenant_id, data_source_id);

CREATE INDEX IF NOT EXISTS embedding_queue_pending_idx
  ON embedding_queue (enqueued_at)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS schema_metadata_quarantine_idx
  ON schema_metadata (tenant_id, quarantine)
  WHERE quarantine = true;

CREATE INDEX IF NOT EXISTS schema_metadata_classified_by_idx
  ON schema_metadata (tenant_id, classified_by);

-- ── Grants ─────────────────────────────────────────────────────────────────────
GRANT SELECT                 ON crawl_runs TO app_read;
GRANT SELECT, INSERT, UPDATE ON crawl_runs TO app_write;
GRANT SELECT, INSERT, UPDATE ON crawl_runs TO app_admin;
GRANT ALL PRIVILEGES         ON crawl_runs TO app_migrator;

GRANT SELECT                 ON inferred_relationships TO app_read;
GRANT SELECT, INSERT, UPDATE ON inferred_relationships TO app_write;
GRANT SELECT, INSERT, UPDATE ON inferred_relationships TO app_admin;
GRANT ALL PRIVILEGES         ON inferred_relationships TO app_migrator;

GRANT SELECT                 ON embedding_queue TO app_read;
GRANT SELECT, INSERT, UPDATE ON embedding_queue TO app_write;
GRANT SELECT, INSERT, UPDATE ON embedding_queue TO app_admin;
GRANT ALL PRIVILEGES         ON embedding_queue TO app_migrator;

COMMIT;
