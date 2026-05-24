# Phase 1 — Control-Plane Database & Migrations

> **Duration:** 2–3 weeks &nbsp; · &nbsp; **Owner:** Backend (PostgreSQL specialist) &nbsp; · &nbsp; **Dependencies:** Phase 0
> **Companion:** [`../implementation-plan.md` §Phase 1](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Land the authoritative data model for the control plane and prove tenant isolation with native Postgres Row-Level Security (RLS) **forced** at the table level. Establish a forward-only migration discipline and tamper-evident audit primitives (hash-chained `policy_audit`) so every subsequent phase inherits a trustworthy substrate.

**Business rationale:** the control-plane DB is the *only* source of truth for who can do what. Any cross-tenant leak, in-place mutation, or unaudited mutation here is a Sev-0 incident. Getting RLS, scoped roles, and tamper-evident audit right in Phase 1 makes every later compliance audit dramatically cheaper.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Migration tooling, conventions, CI integration.
- Schema for: `tenants`, `users`, `roles`, `user_roles`, `data_sources`, `data_classifications`, `policies`, `policy_audit`, `access_audit`, `schema_metadata`, `doc_chunks`.
- RLS policies on every tenant-scoped table; `FORCE ROW LEVEL SECURITY`.
- Scoped database roles (`app_read`, `app_write`, `app_admin`, `app_migrator`, `app_break_glass`, `app_login_user`).
- Audit trigger producing a hash chain on `policy_audit`.
- Partitioning strategy for `access_audit`.
- Seed script for dev fixtures.

**Out of scope**
- AuthN integration (Phase 2).
- Policy DSL parser/compiler (Phase 3).
- Audit pipeline beyond DB-side hash chain (Phase 5).

**Ownership**
- **Drives:** Backend Lead.
- **Reviews:** Security (RLS correctness), DBA (partitioning, vacuum strategy), Compliance (audit trigger semantics).

---

## 3. Hard Dependencies & Sequencing

- Phase 0 cloud landing zone (managed Postgres or container in dev).
- Phase 0 secret management (DB creds in Vault).
- Phase 0 CI must already include a `migrations` job slot.

Sequencing inside the phase:
1. Pick tooling → 2. Land base tables → 3. Add RLS → 4. Add roles → 5. Add triggers → 6. Add partitions → 7. Seed → 8. Hardening tests.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 1.1 — Migration Tooling

**Choice:** `golang-migrate` (forward-only, hash-verified) for embedded use in Go services, OR `Atlas` for declarative schema. Recommendation: `golang-migrate` for V1 — declarative migrations are nicer until you need to express triggers, partitioned tables, and RLS policies, which require imperative SQL anyway.

Conventions:
- Files `NNNN_description.sql` + `NNNN_description.down.sql`.
- Forward-only in `prod` (down migrations exist for `dev`/`staging` only).
- Every file wrapped in `BEGIN; ... COMMIT;`.
- Every statement idempotent (`IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- CI rejects edits to merged migrations (a `migrations-frozen` check compares hashes).

### 1.2 — Base Schema (apply in this order)

1. **`tenants`** — `id uuid pk`, `slug citext unique`, `plan_tier`, `data_residency`, `compliance_modes jsonb`, `status`, timestamps.
2. **`users`** — `id`, `tenant_id`, `email citext`, `external_idp_subject`, `status`, `attributes jsonb`, `session_invalidated_at`, soft-delete columns.
3. **`roles`** — `id`, `tenant_id`, `name`, `description`, `parent_role_id` (hierarchy), `is_system bool`.
4. **`user_roles`** — composite pk `(user_id, role_id)`, `granted_by`, `granted_at`, optional `expires_at`.
5. **`data_sources`** — `id`, `tenant_id`, `kind` (`postgres|mysql|...`), `connection_secret_ref` (Vault path), `default_db`, `residency_region`, `status`.
6. **`data_classifications`** — `id`, `tenant_id`, `data_source_id`, `table_name`, `column_name`, `classification` (`public|internal|confidential|restricted`), `tags text[]`, `pii_category`.
7. **`policies`** — `id`, `tenant_id`, `version`, `status` (`draft|active|archived`), `effect` (`allow|deny`), `subject_match jsonb`, `resource_match jsonb`, `action`, `conditions jsonb`, `obligations jsonb`, `allowed_columns text[]`, `denied_columns text[]`, `row_filter jsonb`, `created_by`, `approved_by`, `effective_from`, `effective_to`.
8. **`policy_audit`** — append-only, with `prev_hash bytea`, `row_hash bytea`, `tenant_id`, `actor_id`, `action`, `before jsonb`, `after jsonb`, `created_at`.
9. **`access_audit`** — partitioned by day on `created_at`; `user_id`, `tenant_id`, `data_source_id`, `resource`, `action`, `decision`, `reason`, `row_count`, `query_hash`, `duration_ms`, `risk_score`, `metadata jsonb`.
10. **`schema_metadata`** — `id`, `tenant_id`, `data_source_id`, `schema_name`, `table_name`, `column_name`, `data_type`, `nullable`, `description`, `classification_id`, `embedding vector(1536)`, `quarantine bool`, `last_seen_at`. Requires `CREATE EXTENSION IF NOT EXISTS vector;`.
11. **`doc_chunks`** — `id`, `tenant_id`, `corpus_id`, `chunk_text`, `acl_roles text[]`, `acl_users uuid[]`, `acl_attrs jsonb`, `classification`, `embedding vector(1536)`, `metadata jsonb`.

All tables: `created_at`, `updated_at` with triggers, deterministic UUIDv7 IDs (lexicographic ordering for index locality), and `tenant_id` as the **first** index column for every secondary index.

### 1.3 — RLS Enabled *and* Forced

For each tenant-scoped table:
```sql
ALTER TABLE users ENABLE  ROW LEVEL SECURITY;
ALTER TABLE users FORCE   ROW LEVEL SECURITY;
CREATE POLICY tenant_iso ON users
  USING       (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK  (tenant_id = current_setting('app.tenant_id')::uuid);
```

**`FORCE`** is non-negotiable. Without it, table owners (migrations, superusers) bypass RLS, and a maintenance job run as the wrong user exfiltrates cross-tenant data silently. CI enforces a check: every tenant-scoped table grep'd for both `ENABLE` and `FORCE`.

For audit tables, the `WITH CHECK` clause prevents inserts with mismatched tenant; for `policy_audit` and `access_audit`, also add a `BEFORE INSERT` trigger that rejects mismatched `tenant_id`.

### 1.4 — Scoped Database Roles

```sql
CREATE ROLE app_read         NOINHERIT;
CREATE ROLE app_write        NOINHERIT;
CREATE ROLE app_admin        NOINHERIT;
CREATE ROLE app_migrator     NOINHERIT BYPASSRLS;
CREATE ROLE app_break_glass  NOINHERIT BYPASSRLS;
CREATE ROLE app_login_user   NOINHERIT LOGIN;
GRANT app_read, app_write, app_admin, app_break_glass TO app_login_user;

CREATE ROLE app_migration_login LOGIN;
GRANT app_migrator TO app_migration_login;
```

Per-table grants are *least privilege*:
- `app_read` → `SELECT` on read-relevant tables only (never on `policy_audit` writes).
- `app_write` → `SELECT, INSERT` (no `UPDATE` on append-only tables).
- `app_admin` → tenancy-mgmt + policy mutations.
- `app_break_glass` → `BYPASSRLS`; only assumable inside an audited break-glass session (Phase 14).
- `app_migrator` → owns DDL; can never execute `SELECT` against data tables in a production session.

### 1.5 — Hash-Chained Audit Trigger

```sql
CREATE OR REPLACE FUNCTION policy_audit_hash_chain()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE prev BYTEA;
BEGIN
  SELECT row_hash INTO prev
    FROM policy_audit
   WHERE tenant_id = NEW.tenant_id
   ORDER BY id DESC
   LIMIT 1
   FOR UPDATE;                       -- serializes appenders per tenant
  NEW.prev_hash := COALESCE(prev, '\x00'::bytea);
  NEW.row_hash  := digest(
    NEW.prev_hash ||
    convert_to(jsonb_build_object(
      'tenant_id', NEW.tenant_id,
      'actor_id',  NEW.actor_id,
      'action',    NEW.action,
      'before',    NEW.before,
      'after',     NEW.after,
      'created_at',NEW.created_at
    )::text, 'UTF8'),
    'sha256'
  );
  RETURN NEW;
END $$;

CREATE TRIGGER policy_audit_hash_trigger
BEFORE INSERT ON policy_audit
FOR EACH ROW EXECUTE FUNCTION policy_audit_hash_chain();
```

**Notes:**
- Hash inputs are *canonical JSONB* (key order normalized) to keep the chain stable.
- `FOR UPDATE` lock on the predecessor row prevents concurrent appenders from forking the chain per tenant.
- A second function `verify_policy_audit_chain(tenant_id, since)` walks the chain and returns the first divergence; called by the hourly Phase 5 verifier.
- `policy_audit` has `REVOKE UPDATE, DELETE` from all roles; only `app_migrator` can perform schema changes (and even then, never row mutations in prod).

### 1.6 — Partitioning `access_audit`

- Daily range partitions on `created_at`.
- Per-tenant compliance setting controls retention; a nightly job detaches and archives partitions older than retention to S3 (compressed Parquet).
- Use **`pg_partman`** (installed as extension) for partition maintenance, OR a small Go cron that pre-creates partitions 7 days ahead.
- Foreign keys on partitioned tables: keep `tenant_id` as a non-FK indexed column (partitioned tables have FK limitations in older PG versions).

### 1.7 — Indexes & Performance

Every secondary index leads with `tenant_id`:
- `policies(tenant_id, status, version)` partial index on `status='active'`.
- `users(tenant_id, email)`.
- `user_roles(tenant_id, user_id)` and `(tenant_id, role_id)`.
- `access_audit(tenant_id, user_id, created_at desc)` per partition.
- `schema_metadata(tenant_id, data_source_id, table_name)`.
- `schema_metadata USING ivfflat(embedding vector_cosine_ops)` once data lands (Phase 7).

**Concurrency tip:** all index creation in prod uses `CREATE INDEX CONCURRENTLY`, run outside the migration transaction (special migration mode).

### 1.8 — Seed Data

`scripts/seed.ts` (idempotent, `ON CONFLICT DO NOTHING`):
- 1 tenant `acme`
- 2 users (`admin@acme.test`, `analyst@acme.test`)
- 2 roles (`admin`, `analyst`)
- 1 demo `data_source` pointing at a containerized `orders_db` PG
- 5 demo policies covering allow, deny, row-filter, column-mask, obligation
- 1 classified column set in `data_classifications`

### 1.9 — CI Hooks

- `migrations-frozen` — fails PR if a previously-merged file content hash changed.
- `migrations-down-only-dev` — fails PR if `.down.sql` is referenced in a production deploy workflow.
- `rls-enforced` — fails PR if a new tenant-scoped table lacks `ENABLE` *and* `FORCE`.
- `naming-conventions` — snake_case columns, plural table names, FKs named `<column>_fk`.
- `seed-idempotent` — runs seed twice; assertion: no duplicate rows.

---

## 5. Architectural Gaps & Missing Requirements

1. **Soft-delete semantics undefined.** Decide: `deleted_at` column vs. tombstone row. Recommendation: `deleted_at` + partial indexes excluding deleted rows.
2. **Multi-region replication strategy.** Logical replication of all tables, or only authoritative? Decide before Phase 15; design `replica identity` now to avoid migrations later.
3. **Encryption at rest with tenant-scoped CMKs.** Postgres TDE is whole-DB; per-tenant CMK requires application-layer envelope encryption (sensitive columns only). Decide which columns qualify.
4. **`session_invalidated_at` semantics.** Phase 2 uses it; Phase 1 must add the column with sensible default (`NULL` = never invalidated) and a check constraint.
5. **`policies.version` strategy.** Monotonic per-tenant? Per-policy-name? Recommendation: per-`(tenant_id, name)` monotonic version; old versions never deleted.
6. **JSONB schema enforcement.** Postgres won't validate `conditions jsonb`; rely on Phase 3 validator but add a `CHECK (jsonb_typeof(conditions) IN ('object','null'))` as cheap insurance.
7. **`access_audit` cardinality.** Will blow up under load; partitioning helps but consider sub-partitioning by `tenant_id` for largest tenants (Phase 15).
8. **GDPR right-to-erasure on audit rows.** Audit cannot be deleted; use deterministic HMAC tokenization on PII columns. Schema must include `actor_token text` populated by app; original `actor_id` is *not* stored in audit-pipeline sinks.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                              | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `SET app.tenant_id` missing from a transaction                        | RLS returns empty result; integration test asserts zero rows + Phase 0 lint flags bare `SET`.    |
| Concurrent insert into `policy_audit` for same tenant                 | `FOR UPDATE` lock on predecessor; tests with 100 goroutines verify monotonic chain.              |
| Migration applied out of order                                        | `schema_migrations` table enforces sequence; CI guard.                                            |
| `pg_partman` extension absent in managed DB                           | Fallback: a Go cron creates the next 7 days of partitions; runbook documents.                    |
| Vacuum / autovacuum freeze on `access_audit` causes wraparound risk   | Per-partition autovacuum tuning + per-week freeze monitoring alert.                              |
| `pgvector` extension version skew between dev and prod                | Pin in migration: `CREATE EXTENSION vector VERSION '0.7.0'`.                                     |
| Time skew between hash chain rows (DST, NTP)                          | All `created_at` in UTC; DB enforces `timezone='UTC'`; clock-skew alarm in Phase 0.              |
| `SET LOCAL` outside a transaction silently no-ops                     | Helper `db.withSession` always opens explicit txn; lint forbids raw connection use.              |
| Long-running transaction blocks DDL                                   | Migrations use `SET lock_timeout = '5s'`; retry strategy with backoff.                           |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Per-tenant partition sub-strategy for `access_audit` at large tenants (Phase 15 territory; design space reserved).
- `policies` table read-heavy → covered by Phase 3 PDP cache; index includes `(tenant_id, status)` for fast active-set query.
- `schema_metadata` IVFFlat index requires re-build after major data churn — runbook documents.

### 7.2 Security
- `BYPASSRLS` roles audited at every grant; alert if a non-system role acquires it.
- `pg_hba.conf` allows only `app_login_user` and `app_migration_login` from app subnets; superuser disabled in prod.
- Connection strings carry no superuser creds anywhere.
- Column-level GRANT minimization: `app_read` cannot `SELECT` from `policy_audit.before/after` directly — only via a `SECURITY DEFINER` function that strips PII.

### 7.3 Multi-Tenant Isolation
- RLS + `FORCE` + scoped roles + `SET LOCAL` discipline = **four independent layers**. Each test case verifies the failure mode of each layer in isolation.
- Add chaos test: a buggy service forgets to set `app.tenant_id` → result is zero rows, *never* cross-tenant.

### 7.4 Concurrency
- Hash-chain `FOR UPDATE` serializes per tenant; benchmarked at ≥ 2k policy_audit appends/sec per tenant.
- All schema changes use `CREATE INDEX CONCURRENTLY`, `ALTER TABLE ... NOT VALID` + `VALIDATE`, and short `lock_timeout` to avoid head-of-line blocking.

### 7.5 Performance
- Bench p95 for a `policies` active-set read with tenant filter: < 2 ms with warm cache.
- Bench `policy_audit` append: < 5 ms p99 including hash compute.
- Bench `access_audit` insert: < 1 ms p99 (no chain).

---

## 8. Recommended Improvements

### Architecture
- Introduce a **`policy_set`** concept (versioned bundle) — every active-policy switch is an atomic swap of a `policy_set_version` per tenant. Simpler invalidation in Phase 3.
- Reserve a `tenant_id IS NOT NULL` check constraint on every tenant-scoped table — even though FK ensures presence, the check makes intent explicit and prevents accidental NULL via `ALTER`.

### DX
- **Repository SQL linter** (`squawk`) enforces "no unsafe migrations" rules (no `ALTER COLUMN TYPE` without batched plan, etc.).
- `make psql` opens a `psql` shell as `app_login_user` with `app.tenant_id` pre-set to a fixture tenant, so engineers reproduce RLS behavior locally.

### UX
- N/A for this phase; downstream `admin-console` will surface schema/RLS state in Phase 4.

### Reliability
- Hourly `verify_policy_audit_chain` cron in CI for dev/staging from this phase forward; Phase 5 wires the prod version.
- Backups: WAL archiving from day one in `dev`/`staging` so backup tooling is exercised long before prod.

### Observability
- `pg_stat_statements` enabled, dashboards for top queries by `total_exec_time` per tenant.
- `auto_explain` for slow queries > 200 ms.
- Per-table size, bloat, index hit-ratio dashboards.

### Maintainability
- Generated ER diagram (via `pg-dot` or `schemaspy`) updated on every migration; committed to repo.
- ADR for each major schema decision (e.g., "UUIDv7 vs ULID", "JSONB conditions vs columns").

---

## 9. Technical Considerations

### 9.1 DB Design
Covered above. Key invariants:
- Every tenant-scoped row is `tenant_id`-prefixed in every secondary index.
- Append-only tables (`policy_audit`, `access_audit`) reject `UPDATE`/`DELETE` at the role level.

### 9.2 API Contracts
None at this layer. But the SQL surface area exposed to services is contractualized: all data access via repository packages, no ad-hoc SQL in handlers.

### 9.3 RBAC (DB-level)
The role hierarchy described in 1.4 is the *first* RBAC layer the system has — it's complementary to the application RBAC built in Phase 3.

### 9.4 Validation Flows
- `policies.conditions` validated downstream by Phase 3 DSL.
- `data_classifications.classification` enforced via PG `ENUM` or `CHECK`.
- All `email` columns use `citext`.

### 9.5 Caching
None at DB layer. Phase 3 caches policy decisions.

### 9.6 Queues & Background Jobs
- Partition pre-creation cron.
- Nightly hash-chain verifier (full pass, sample-based in `prod`).
- Nightly partition archiver to S3.

### 9.7 Audit Logs
The DB-side hash chain is the *ground truth*. Phase 5 mirrors to WORM + ClickHouse without changing this fact.

### 9.8 Retry & Idempotency
- All migrations idempotent.
- Repository layer uses idempotency keys (UUID) on mutating ops; deduplicated by a `(tenant_id, idempotency_key)` unique index where applicable.

### 9.9 Monitoring
- `pg_locks` exporter (Prometheus).
- Replication lag exporter.
- Long-running transaction alert (> 30s in `prod`).
- Vacuum freeze countdown alert.

### 9.10 CI/CD
- PR migration step runs `migrate up` then `migrate down` then `migrate up` on a fresh PG container.
- Integration job loads the seed and asserts RLS denies cross-tenant queries.
- Performance regression job: `pgbench`-like microbench on key queries; fails if p95 regresses > 20%.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                 | Likelihood | Impact   | Mitigation                                                                                  |
| -------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------- |
| Forgetting `FORCE ROW LEVEL SECURITY`                                | Med        | Critical | CI lint; integration test asserts cross-tenant denial.                                       |
| PgBouncer transaction pooling + bare `SET` (not `SET LOCAL`) leaks GUCs | High      | Critical | Repository helper enforces `SET LOCAL` in explicit txns; lint forbids bare `SET app.*`.    |
| Hash-chain divergence under partition failover                       | Low        | High     | `FOR UPDATE` serializes appenders; multi-region writes only allowed to primary in Phase 15.|
| Schema breaking change shipped non-expand-contract                   | Med        | High     | `squawk` lint + 2-reviewer rule for any column drop / type change.                          |
| `pgvector` ANN index degrades silently as data grows                 | Med        | Med      | Quarterly rebuild scheduled; index hit-ratio dashboard.                                      |
| Cross-tenant FK accidentally introduced                              | Low        | Critical | CI guard: every FK target must be tenant-scoped or whitelisted.                              |

### Rollback
- Forward-only in `prod`. To revert a bad migration, **roll forward** with a compensating migration.
- `dev`/`staging` allow `.down.sql` to ease iteration.
- Backups verified daily via automated restore-to-scratch.

### Future Extensibility
- **Multi-region writes:** the chain trigger uses `FOR UPDATE`; multi-master would require chain-merging logic. Defer to Phase 15 with a single-writer model.
- **Per-tenant CMK encryption:** add `kms_key_arn` column on `tenants`; sensitive-column writes encrypt with envelope encryption (Phase 16 for HIPAA).
- **CDC for managed DB sync:** reserve `wal_level=logical` from day one in cloud Postgres parameter groups.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Full schema migration set, applied cleanly from empty DB.
- [ ] All tenant-scoped tables have `ENABLE` + `FORCE` RLS.
- [ ] Scoped roles created; per-role least-privilege grants applied.
- [ ] Hash-chain trigger live; `verify_policy_audit_chain` function deployed.
- [ ] `access_audit` partitioned; auto-create + retention jobs operational.
- [ ] `pgvector` extension installed and pinned.
- [ ] Seed script idempotent and exercised in CI.
- [ ] ER diagram committed.
- [ ] CI guards: migrations-frozen, RLS-enforced, naming, seed-idempotent.

### Acceptance Criteria
- [ ] Inserting a `policy_audit` row produces a non-null `row_hash`.
- [ ] Tampering with a historical `policy_audit` row breaks the chain (verifier detects).
- [ ] Removing `SET LOCAL app.tenant_id` from any query returns 0 rows (regression test).
- [ ] Cross-tenant select returns 0 rows under all role contexts except `app_break_glass`.
- [ ] Migration rollback works in `dev`; forward-only enforced in `prod`.

---

## 12. Production Readiness Checklist

- [ ] Backups configured + restore verified on a synthetic env.
- [ ] WAL archive to S3 enabled in `staging`/`prod`.
- [ ] `pg_stat_statements` + `auto_explain` enabled.
- [ ] Alert rules: replication lag, long txn, freeze countdown, deadlock count.
- [ ] Vacuum/autovacuum tuned per partitioned table.
- [ ] DR runbook drafted (failover, restore, hash-chain re-verification).
- [ ] Tenant onboarding runbook drafted: how to create a tenant + initial roles + scoped Vault secret path.

---

## 13. Remaining Risks Carried Forward

- **No application code yet trusts the schema** — Phase 2 introduces SessionContext; until then, RLS is unprotected by app-side validation.
- **Audit pipeline absent** — DB-side chain is intact, but no WORM mirror until Phase 5; an attacker with DB access could still attempt tampering before Phase 5 verifier exists.
- **Per-tenant CMK encryption** unimplemented (Phase 16).
- **Cross-region replication** unimplemented (Phase 15).
- **PgBouncer config** is dev-only; pooling-mode discipline assumed but not yet enforced by infra.
