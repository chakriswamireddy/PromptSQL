-- 0011_indexes.up.sql
-- Secondary indexes.  All lead with tenant_id for partition-pruning efficiency.
-- CONCURRENTLY cannot run inside a transaction; these are run individually after
-- CREATE TABLE is complete.  In CI the migration runner applies them sequentially
-- outside a transaction block using the "no-txn" marker comment below.
-- In production, run with `make migrate` which handles the no-txn flag automatically.

-- [no-txn]

-- ── tenants ───────────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenants_slug
  ON tenants (slug);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tenants_status
  ON tenants (status) WHERE deleted_at IS NULL;

-- ── users ─────────────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_tenant_email
  ON users (tenant_id, email);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_tenant_status
  ON users (tenant_id, status) WHERE deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_idp_subject
  ON users (external_idp_subject) WHERE external_idp_subject IS NOT NULL;

-- ── roles ─────────────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_roles_tenant
  ON roles (tenant_id, name);

-- ── user_roles ────────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_roles_tenant_user
  ON user_roles (tenant_id, user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_roles_tenant_role
  ON user_roles (tenant_id, role_id);

-- ── data_sources ──────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_data_sources_tenant_status
  ON data_sources (tenant_id, status);

-- ── data_classifications ──────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_data_class_tenant_source
  ON data_classifications (tenant_id, data_source_id, table_name);

-- ── policies ──────────────────────────────────────────────────────────────────
-- Hot path: PDP active-set query — p95 target < 2 ms with warm cache
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policies_tenant_active
  ON policies (tenant_id, status, version DESC) WHERE status = 'active';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policies_tenant_name
  ON policies (tenant_id, name, version DESC);

-- ── policy_audit ──────────────────────────────────────────────────────────────
-- Chain-walk query (verify_policy_audit_chain) needs ordered scan per tenant
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policy_audit_tenant_created
  ON policy_audit (tenant_id, created_at ASC, id ASC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policy_audit_tenant_actor
  ON policy_audit (tenant_id, actor_id, created_at DESC);

-- ── schema_metadata ───────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_schema_meta_tenant_source_table
  ON schema_metadata (tenant_id, data_source_id, table_name);

-- ── doc_chunks ────────────────────────────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_doc_chunks_tenant_corpus
  ON doc_chunks (tenant_id, corpus_id);
