-- 0005_data_layer_tables.up.sql
-- data_sources, data_classifications
BEGIN;

SET lock_timeout = '5s';

-- ── data_sources ──────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS data_sources (
  id                    uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id             uuid        NOT NULL,
  kind                  text        NOT NULL,
  display_name          text        NOT NULL,
  -- Vault path; the actual secret is never stored in the DB
  connection_secret_ref text        NOT NULL,
  default_db            text,
  residency_region      text        NOT NULL DEFAULT 'us-east-1',
  status                text        NOT NULL DEFAULT 'active',
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT data_sources_pkey          PRIMARY KEY (id),
  CONSTRAINT data_sources_tenant_id_nn  CHECK (tenant_id IS NOT NULL),
  CONSTRAINT data_sources_kind_ck       CHECK (kind IN (
    'postgres','mysql','sqlserver','oracle',
    'snowflake','bigquery','databricks','mongodb'
  )),
  CONSTRAINT data_sources_status_ck     CHECK (status IN ('active','disabled','error')),
  CONSTRAINT data_sources_tenant_id_fk  FOREIGN KEY (tenant_id) REFERENCES tenants(id)
);

CREATE TRIGGER data_sources_set_updated_at
  BEFORE UPDATE ON data_sources
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── data_classifications ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS data_classifications (
  id             uuid        NOT NULL DEFAULT gen_uuidv7(),
  tenant_id      uuid        NOT NULL,
  data_source_id uuid        NOT NULL,
  schema_name    text,
  table_name     text        NOT NULL,
  column_name    text        NOT NULL,
  classification text        NOT NULL DEFAULT 'internal',
  tags           text[]      NOT NULL DEFAULT '{}',
  pii_category   text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT data_classifications_pkey           PRIMARY KEY (id),
  CONSTRAINT data_classifications_tenant_id_nn   CHECK (tenant_id IS NOT NULL),
  CONSTRAINT data_classifications_class_ck       CHECK (classification IN (
    'public','internal','confidential','restricted'
  )),
  CONSTRAINT data_classifications_unique         UNIQUE (tenant_id, data_source_id, schema_name, table_name, column_name),
  CONSTRAINT data_classifications_tenant_fk      FOREIGN KEY (tenant_id)      REFERENCES tenants(id),
  CONSTRAINT data_classifications_source_fk      FOREIGN KEY (data_source_id) REFERENCES data_sources(id)
);

CREATE TRIGGER data_classifications_set_updated_at
  BEFORE UPDATE ON data_classifications
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── per-table grants ──────────────────────────────────────────────────────────
GRANT SELECT                 ON data_sources        TO app_read;
GRANT SELECT, INSERT, UPDATE ON data_sources        TO app_admin;

GRANT SELECT                 ON data_classifications TO app_read;
GRANT SELECT, INSERT, UPDATE ON data_classifications TO app_write;
GRANT SELECT, INSERT, UPDATE ON data_classifications TO app_admin;

GRANT ALL PRIVILEGES ON data_sources, data_classifications TO app_migrator;

COMMIT;
