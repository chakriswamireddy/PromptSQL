-- 0011_indexes.down.sql  (dev/staging only)
-- [no-txn]
DROP INDEX CONCURRENTLY IF EXISTS idx_doc_chunks_tenant_corpus;
DROP INDEX CONCURRENTLY IF EXISTS idx_schema_meta_tenant_source_table;
DROP INDEX CONCURRENTLY IF EXISTS idx_policy_audit_tenant_actor;
DROP INDEX CONCURRENTLY IF EXISTS idx_policy_audit_tenant_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_policies_tenant_name;
DROP INDEX CONCURRENTLY IF EXISTS idx_policies_tenant_active;
DROP INDEX CONCURRENTLY IF EXISTS idx_data_class_tenant_source;
DROP INDEX CONCURRENTLY IF EXISTS idx_data_sources_tenant_status;
DROP INDEX CONCURRENTLY IF EXISTS idx_user_roles_tenant_role;
DROP INDEX CONCURRENTLY IF EXISTS idx_user_roles_tenant_user;
DROP INDEX CONCURRENTLY IF EXISTS idx_roles_tenant;
DROP INDEX CONCURRENTLY IF EXISTS idx_users_idp_subject;
DROP INDEX CONCURRENTLY IF EXISTS idx_users_tenant_status;
DROP INDEX CONCURRENTLY IF EXISTS idx_users_tenant_email;
DROP INDEX CONCURRENTLY IF EXISTS idx_tenants_status;
DROP INDEX CONCURRENTLY IF EXISTS idx_tenants_slug;
