-- 0008_schema_metadata_doc_chunks.up.sql
-- schema_metadata (with pgvector embedding column) and doc_chunks (RAG corpus).
BEGIN;

SET lock_timeout = '5s';

-- ── schema_metadata ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS schema_metadata (
  id                uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id         uuid        NOT NULL,
  data_source_id    uuid        NOT NULL,
  schema_name       text        NOT NULL DEFAULT 'public',
  table_name        text        NOT NULL,
  column_name       text        NOT NULL,
  data_type         text        NOT NULL,
  nullable          boolean     NOT NULL DEFAULT true,
  description       text,
  classification_id uuid,
  -- Populated by Phase 7 schema crawler; NULL until first crawl
  embedding         vector(1536),
  quarantine        boolean     NOT NULL DEFAULT false,
  last_seen_at      timestamptz NOT NULL DEFAULT now(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT schema_metadata_pkey            PRIMARY KEY (id),
  CONSTRAINT schema_metadata_tenant_id_nn    CHECK (tenant_id IS NOT NULL),
  CONSTRAINT schema_metadata_unique          UNIQUE (tenant_id, data_source_id, schema_name, table_name, column_name),
  CONSTRAINT schema_metadata_tenant_fk       FOREIGN KEY (tenant_id)         REFERENCES tenants(id),
  CONSTRAINT schema_metadata_source_fk       FOREIGN KEY (data_source_id)    REFERENCES data_sources(id),
  CONSTRAINT schema_metadata_class_fk        FOREIGN KEY (classification_id) REFERENCES data_classifications(id)
);

CREATE TRIGGER schema_metadata_set_updated_at
  BEFORE UPDATE ON schema_metadata
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- IVFFlat ANN index created by Phase 7 after initial data load (requires data to tune lists).
-- The DDL placeholder is commented so CI doesn't try to build it on empty table.
-- CREATE INDEX CONCURRENTLY schema_metadata_embedding_idx
--   ON schema_metadata USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

-- ── doc_chunks ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS doc_chunks (
  id             uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id      uuid        NOT NULL,
  corpus_id      uuid        NOT NULL,
  chunk_text     text        NOT NULL,
  acl_roles      text[]      NOT NULL DEFAULT '{}',
  acl_users      uuid[]      NOT NULL DEFAULT '{}',
  acl_attrs      jsonb       NOT NULL DEFAULT '{}',
  classification text        NOT NULL DEFAULT 'internal',
  -- Populated by Phase 8 retrieval service
  embedding      vector(1536),
  metadata       jsonb       NOT NULL DEFAULT '{}',
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT doc_chunks_pkey          PRIMARY KEY (id),
  CONSTRAINT doc_chunks_tenant_id_nn  CHECK (tenant_id IS NOT NULL),
  CONSTRAINT doc_chunks_class_ck      CHECK (classification IN (
    'public','internal','confidential','restricted'
  )),
  CONSTRAINT doc_chunks_acl_obj_ck    CHECK (jsonb_typeof(acl_attrs) IN ('object','null')),
  CONSTRAINT doc_chunks_meta_obj_ck   CHECK (jsonb_typeof(metadata)  IN ('object','null')),
  CONSTRAINT doc_chunks_tenant_fk     FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE TRIGGER doc_chunks_set_updated_at
  BEFORE UPDATE ON doc_chunks
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── per-table grants ──────────────────────────────────────────────────────────
GRANT SELECT                 ON schema_metadata TO app_read;
GRANT SELECT, INSERT, UPDATE ON schema_metadata TO app_write;
GRANT SELECT, INSERT, UPDATE ON schema_metadata TO app_admin;

GRANT SELECT                 ON doc_chunks TO app_read;
GRANT SELECT, INSERT, UPDATE ON doc_chunks TO app_write;
GRANT SELECT, INSERT, UPDATE ON doc_chunks TO app_admin;

GRANT ALL PRIVILEGES ON schema_metadata, doc_chunks TO app_migrator;

COMMIT;
