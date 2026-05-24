-- 0019_retrieval_config.up.sql
-- Phase 8: corpora, tenant_vector_stores, llm_provider_routes, tenant_denylist;
--          extend doc_chunks with quarantine + ingested_at.
BEGIN;

SET lock_timeout = '5s';

-- ── corpora ────────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS corpora (
  id                    uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id             uuid        NOT NULL,
  name                  text        NOT NULL,
  description           text        NOT NULL DEFAULT '',
  classification        text        NOT NULL DEFAULT 'internal',
  embedding_model       text        NOT NULL DEFAULT 'text-embedding-3-small',
  quarantine_hold_hours int         NOT NULL DEFAULT 24,
  pause_retrieval       boolean     NOT NULL DEFAULT false,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT corpora_pkey              PRIMARY KEY (id),
  CONSTRAINT corpora_tid_nn            CHECK (tenant_id IS NOT NULL),
  CONSTRAINT corpora_class_ck          CHECK (classification IN ('public','internal','confidential','restricted')),
  CONSTRAINT corpora_hold_ck           CHECK (quarantine_hold_hours >= 0),
  CONSTRAINT corpora_tenant_fk         FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT corpora_name_unique       UNIQUE (tenant_id, name)
);

CREATE TRIGGER corpora_set_updated_at
  BEFORE UPDATE ON corpora
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE corpora ENABLE ROW LEVEL SECURITY;
ALTER TABLE corpora FORCE ROW LEVEL SECURITY;

CREATE POLICY corpora_tenant_isolation ON corpora
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── tenant_vector_stores ───────────────────────────────────────────────────────
-- Per-tenant private vector store config for 'restricted' corpora.
CREATE TABLE IF NOT EXISTS tenant_vector_stores (
  id          uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id   uuid        NOT NULL,
  kind        text        NOT NULL DEFAULT 'pgvector',
  endpoint    text,
  kms_key_ref text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tenant_vector_stores_pkey    PRIMARY KEY (id),
  CONSTRAINT tenant_vector_stores_kind_ck CHECK (kind IN ('pgvector','qdrant')),
  CONSTRAINT tenant_vector_stores_tid_fk  FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT tenant_vector_stores_unique  UNIQUE (tenant_id)
);

CREATE TRIGGER tenant_vector_stores_set_updated_at
  BEFORE UPDATE ON tenant_vector_stores
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE tenant_vector_stores ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_vector_stores FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_vector_stores_isolation ON tenant_vector_stores
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── llm_provider_routes ────────────────────────────────────────────────────────
-- Per-tenant, per-classification LLM provider routing table.
CREATE TABLE IF NOT EXISTS llm_provider_routes (
  id               uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id        uuid        NOT NULL,
  classification   text        NOT NULL,
  provider_name    text        NOT NULL,
  model            text        NOT NULL,
  priority         int         NOT NULL DEFAULT 1,
  zero_retention   boolean     NOT NULL DEFAULT false,
  private_only     boolean     NOT NULL DEFAULT false,
  residency_region text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT llm_routes_pkey    PRIMARY KEY (id),
  CONSTRAINT llm_routes_class_ck CHECK (classification IN ('public','internal','confidential','restricted')),
  CONSTRAINT llm_routes_pri_ck   CHECK (priority > 0),
  CONSTRAINT llm_routes_tid_fk   FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT llm_routes_unique   UNIQUE (tenant_id, classification, provider_name)
);

CREATE TRIGGER llm_provider_routes_set_updated_at
  BEFORE UPDATE ON llm_provider_routes
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE llm_provider_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE llm_provider_routes FORCE ROW LEVEL SECURITY;

CREATE POLICY llm_provider_routes_isolation ON llm_provider_routes
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── tenant_denylist ────────────────────────────────────────────────────────────
-- Admin-configurable string denylist for injection-defense preprocessing.
CREATE TABLE IF NOT EXISTS tenant_denylist (
  id          uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id   uuid        NOT NULL,
  corpus_id   uuid,
  phrase      text        NOT NULL,
  created_by  uuid        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tenant_denylist_pkey      PRIMARY KEY (id),
  CONSTRAINT tenant_denylist_phrase_nn CHECK (length(phrase) > 0),
  CONSTRAINT tenant_denylist_tid_fk    FOREIGN KEY (tenant_id) REFERENCES tenants(id),
  CONSTRAINT tenant_denylist_corpus_fk FOREIGN KEY (corpus_id) REFERENCES corpora(id),
  CONSTRAINT tenant_denylist_unique    UNIQUE (tenant_id, corpus_id, phrase)
);

ALTER TABLE tenant_denylist ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_denylist FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_denylist_isolation ON tenant_denylist
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ── Extend doc_chunks ──────────────────────────────────────────────────────────
-- New-chunk quarantine (released after corpus.quarantine_hold_hours).
ALTER TABLE doc_chunks
  ADD COLUMN IF NOT EXISTS quarantine  boolean     NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS ingested_at timestamptz NOT NULL DEFAULT now();

-- FK from doc_chunks.corpus_id to corpora — add only if corpus_id column exists.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_name = 'doc_chunks' AND column_name = 'corpus_id'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.table_constraints
     WHERE table_name = 'doc_chunks' AND constraint_name = 'doc_chunks_corpus_fk'
  ) THEN
    ALTER TABLE doc_chunks
      ADD CONSTRAINT doc_chunks_corpus_fk FOREIGN KEY (corpus_id) REFERENCES corpora(id);
  END IF;
END$$;

-- ── Indexes ────────────────────────────────────────────────────────────────────
-- pgvector ANN index for non-quarantined doc_chunks.
CREATE INDEX IF NOT EXISTS doc_chunks_embedding_cosine_idx
  ON doc_chunks USING ivfflat (embedding vector_cosine_ops)
  WITH (lists = 100)
  WHERE quarantine = false;

CREATE INDEX IF NOT EXISTS doc_chunks_corpus_tenant_idx
  ON doc_chunks (tenant_id, corpus_id, quarantine, ingested_at DESC);

CREATE INDEX IF NOT EXISTS doc_chunks_acl_users_gin_idx
  ON doc_chunks USING GIN (acl_users);

CREATE INDEX IF NOT EXISTS doc_chunks_acl_roles_gin_idx
  ON doc_chunks USING GIN (acl_roles);

CREATE INDEX IF NOT EXISTS corpora_tenant_class_idx
  ON corpora (tenant_id, classification);

CREATE INDEX IF NOT EXISTS llm_routes_tenant_class_pri_idx
  ON llm_provider_routes (tenant_id, classification, priority ASC);

CREATE INDEX IF NOT EXISTS tenant_denylist_tenant_corpus_idx
  ON tenant_denylist (tenant_id, corpus_id);

-- ── Quarantine auto-release function ──────────────────────────────────────────
CREATE OR REPLACE FUNCTION release_quarantined_chunks(p_tenant_id uuid DEFAULT NULL)
  RETURNS int
  LANGUAGE plpgsql SECURITY DEFINER
AS $$
DECLARE
  released int := 0;
BEGIN
  UPDATE doc_chunks dc
     SET quarantine = false
    FROM corpora c
   WHERE dc.corpus_id = c.id
     AND dc.quarantine = true
     AND dc.ingested_at < now() - make_interval(hours => c.quarantine_hold_hours)
     AND (p_tenant_id IS NULL OR dc.tenant_id = p_tenant_id);
  GET DIAGNOSTICS released = ROW_COUNT;
  RETURN released;
END;
$$;

-- ── Grants ─────────────────────────────────────────────────────────────────────
GRANT SELECT                 ON corpora TO app_read;
GRANT SELECT, INSERT, UPDATE ON corpora TO app_write;
GRANT SELECT, INSERT, UPDATE ON corpora TO app_admin;
GRANT ALL PRIVILEGES         ON corpora TO app_migrator;

GRANT SELECT                 ON tenant_vector_stores TO app_read;
GRANT SELECT, INSERT, UPDATE ON tenant_vector_stores TO app_write;
GRANT ALL PRIVILEGES         ON tenant_vector_stores TO app_migrator;

GRANT SELECT                 ON llm_provider_routes TO app_read;
GRANT SELECT, INSERT, UPDATE ON llm_provider_routes TO app_write;
GRANT SELECT, INSERT, UPDATE ON llm_provider_routes TO app_admin;
GRANT ALL PRIVILEGES         ON llm_provider_routes TO app_migrator;

GRANT SELECT                 ON tenant_denylist TO app_read;
GRANT SELECT, INSERT, UPDATE ON tenant_denylist TO app_write;
GRANT SELECT, INSERT, UPDATE ON tenant_denylist TO app_admin;
GRANT ALL PRIVILEGES         ON tenant_denylist TO app_migrator;

COMMIT;
