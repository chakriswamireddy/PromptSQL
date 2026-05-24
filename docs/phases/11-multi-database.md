# Phase 11 — Multi-Database Expansion

> **Duration:** 23–28 weeks (≈8–10 weeks focused) &nbsp; · &nbsp; **Owner:** Backend (per-DB specialists) &nbsp; · &nbsp; **Dependencies:** Phases 0–10
> **Companion:** [`../implementation-plan.md` §Phase 11](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Take the PEP, schema catalog, retrieval, and AI graphs cross-engine: MySQL, SQL Server, Oracle, Snowflake, BigQuery, Databricks, and MongoDB are all governed by **the same policies** authored once. A single policy authored against `payments.amount` produces semantically equivalent enforcement in every dialect.

**Business rationale:** customers do not run one database. Selling beyond PostgreSQL is a precondition for enterprise deals — every prospect of meaningful size mixes Snowflake + Postgres + a legacy Oracle. "One policy, every database" is the marketable claim that justifies premium tier pricing.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Two waves:
  - **Wave A — Wire-protocol or JDBC bridge:** MySQL, SQL Server, Oracle (5–6 weeks).
  - **Wave B — REST / API proxy:** Snowflake, BigQuery, Databricks (3–4 weeks).
- MongoDB via a separate wire proxy + aggregation-pipeline injection.
- Per-engine native last-line enforcement (RLS / VPD / Row Access Policies / DDM / Policy Tags / `$match` injection).
- Per-engine SessionContext propagation.
- Calcite dialect coverage and policy semantic-equivalence tests.

**Out of scope**
- Streaming/CDC engines (Kafka, Pulsar) as data sources.
- NoSQL beyond MongoDB (e.g., DynamoDB, Cassandra) — reserve for Phase 11.5.
- Embedded analytics engines (DuckDB) — deferred.
- Cross-engine joins via federation — deferred.

**Ownership**
- **Drives:** Backend Lead with per-engine specialists.
- **Reviews:** Security (per-engine native enforcement), DBA per engine, Calcite expert.

---

## 3. Hard Dependencies & Sequencing

- Phase 6 proxy abstractions.
- Phase 7 crawler abstractions.
- Phase 8 snapshot generation engine-agnostic.
- Phase 3 PDP SQL emitter dialect-aware via Calcite.

Sequencing per engine: connector → crawler → proxy (wire or REST) → native last-line enforcement → equivalence tests → admin UI dropdowns → general availability per engine.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 11.1 — Engine Coverage Matrix

| Engine        | Proxy strategy            | Native last-line                  | Crawler         | Calcite dialect       | Wave |
| ------------- | ------------------------- | --------------------------------- | --------------- | --------------------- | ---- |
| MySQL/MariaDB | ProxySQL extension or Go wire | Views + role grants            | info_schema     | `MysqlSqlDialect`     | A    |
| SQL Server    | JDBC bridge (TDS hard)    | RLS + DDM                         | INFORMATION_SCHEMA / sys.* | `MssqlSqlDialect`         | A    |
| Oracle        | JDBC bridge               | VPD (DBMS_RLS)                    | ALL_TAB_COLUMNS | `OracleSqlDialect`    | A    |
| Snowflake     | REST proxy + JDBC         | Row Access Policies + DDM         | INFORMATION_SCHEMA | `SnowflakeSqlDialect` | B    |
| BigQuery      | REST proxy                | Row-level access + Policy Tags    | INFORMATION_SCHEMA | `BigQuerySqlDialect`  | B    |
| Databricks    | JDBC bridge               | Unity Catalog row filters + DDM   | system.information_schema | `SparkSqlDialect` | B   |
| MongoDB       | Wire proxy                | `$match` injection in aggregation | listCollections + sample inference | n/a (no SQL) | A/B |

### 11.2 — Connector Abstraction

`pkg/connectors`:

```go
type Connector interface {
  Connect(ctx, *DataSource) (*Connection, error)
  Crawl(ctx, *Connection) (*CatalogDelta, error)
  EnforceContext(ctx, *Connection, *SessionContext) error   // set session vars
  PrepareUDFs(ctx, *Connection) error                       // mask_* functions
  SyncNativePolicies(ctx, *Connection, []*Policy) error
  Execute(ctx, *Connection, *Query) (*ResultStream, error)
  Close(ctx, *Connection) error
}
```

Each engine implements the interface; the proxy + crawler + syncer are engine-agnostic at the orchestration layer.

### 11.3 — Per-Engine SessionContext Propagation

| Engine        | How to propagate user/tenant context                                                  |
| ------------- | ------------------------------------------------------------------------------------- |
| MySQL         | `SET @app_user = '…'; SET @app_tenant = '…'; SET @app_campus = '…'`                  |
| SQL Server    | `EXEC sp_set_session_context @key='user', @value='…'; @key='tenant', @value='…'`     |
| Oracle        | `DBMS_SESSION.SET_IDENTIFIER('user_uuid')`; access via `SYS_CONTEXT('USERENV', 'CLIENT_IDENTIFIER')` or custom application context  |
| Snowflake     | `SET user_id = '…'; SET tenant_id = '…';` + session policy variables                  |
| BigQuery      | Request labels + IAM with service-account impersonation; per-query labels carry user_id (hashed)  |
| Databricks    | Session variables `SET user_id = '…'`; Unity Catalog uses `current_user()` mapped via SCIM        |
| MongoDB       | `$set` in transactions; or stage `$match` injection in aggregation pipelines           |

Documented per-engine in `docs/connectors/<engine>.md`.

### 11.4 — Native Last-Line Enforcement Syncers

Each engine has a syncer that mirrors active policies into native constructs:

- **MySQL:** generate views per (table, role) + grant only those views.
- **SQL Server:** create predicate-based RLS policies + DDM rules per column.
- **Oracle:** DBMS_RLS policy registration + VPD policy functions; apply column-level fine-grained access control (FGAC).
- **Snowflake:** create row access policies + DDM masking policies; attach via `ALTER TABLE ... ADD ROW ACCESS POLICY`.
- **BigQuery:** apply IAM conditional bindings + Policy Tag taxonomy to columns; row-level access via authorized views.
- **Databricks:** Unity Catalog row filters + column masks via SQL.
- **MongoDB:** intercept aggregation pipeline; insert `$match` stage as first or merge with existing.

Cadence: hourly cron in V1, CDC-driven realtime in Phase 15.

### 11.5 — Calcite Dialect Coverage

Calcite ships `Postgresql`, `Mysql`, `Mssql`, `Oracle`, `Snowflake`, `BigQuery`, `Spark` dialects. Reuse; customize where needed:
- Mask UDF names per engine (`mask_email_domain` is portable; some engines require schema-qualified names).
- Function aliases (`date_trunc` vs `DATEADD`).
- LIMIT vs `TOP` vs `FETCH FIRST n ROWS ONLY`.
- Identifier quoting differences.

### 11.6 — Equivalence Tests

A test harness:
1. Take a corpus of 50+ canonical policies authored once.
2. For each policy + each engine + each canonical query, render the rewrite.
3. Use `sqlglot` (or per-engine round-trip via Calcite) to normalize and compare semantics.
4. Execute against ephemeral engine containers; assert row equivalence.

CI runs the full matrix on each release.

### 11.7 — Crawler Adaptations

- Per-engine information schema variants.
- Per-engine FK and constraint discovery.
- Per-engine sampling syntax (`SAMPLE` clauses, `TABLESAMPLE`, etc.).
- MongoDB: schema inference via `$sample` aggregation + JSON path explosion (depth-limited).

### 11.8 — Authentication & Authorization Per Engine

- Each engine has a Vault-stored credentials path (already in Phase 1 `data_sources.connection_secret_ref`).
- Connection-token flow (Phase 6.5) generalized; per engine ish:
  - MySQL/MSSQL/Oracle: token = DB password substitute.
  - Snowflake/BigQuery/Databricks: signed JWT for REST APIs; service-account impersonation for BigQuery.
  - MongoDB: SCRAM-SHA-256 with token.

### 11.9 — Admin Console: Multi-Engine UX

- Data source creation wizard supports all engines.
- Per-engine help with required permissions for the platform's read-only service account.
- Per-engine capability matrix surface so admins see which features apply.
- Per-engine mask UDF installer (dry-run + apply).

### 11.10 — MongoDB Special Case

MongoDB is **not SQL**; the policy semantics must translate:
- Row filter → first-stage `$match`.
- Column allowlist / denied columns → `$project`.
- Column mask → `$addFields` with mask function (server-side JS forbidden in many deployments; use computed fields).
- Document classification → at collection or document level (custom field).

The AI orchestrator (Phase 10) also extends to MongoDB aggregation language — likely a new graph variant. V1 may ship MongoDB **without** AI authoring for queries; admin authors policies as JSON, end users use Compass / drivers via the proxy.

---

## 5. Architectural Gaps & Missing Requirements

1. **Cross-engine policy authoring UX.** Policies authored against one engine's schema may not be directly portable; the admin console should clearly scope policies per data source kind.
2. **Cross-engine joins / federation.** Postgres FDW, Trino, Athena — defer; reserve a `federated_query` schema.
3. **Per-engine cost gate semantics.** `EXPLAIN` differs widely; BigQuery dry-runs return cost in bytes; Snowflake uses warehouse credits. Abstract `CostEstimate` per engine.
4. **Per-engine row counts.** Plan rows may not be available; fall back to query-history heuristics.
5. **Streaming semantics differ.** BigQuery REST uses paginated responses, not server-pushed streaming; design adaptors.
6. **TDS / TNS wire protocols are gnarly.** JDBC bridge is fine V1; revisit native wire for high-throughput tenants in Phase 15.
7. **Per-engine retention of audit context.** Some engines lack a query-comment mechanism for trace IDs; standardize trace-propagation paths.
8. **Capability matrix UI.** A clear "what works on engine X" matrix in docs *and* the admin UI prevents customer confusion.
9. **MongoDB-specific Calcite-equivalent.** Mongo doesn't have Calcite; build a small AST → pipeline transformer (alternative: use Apache Drill semantics).
10. **Vendor lock-in mitigation.** Some engines (Snowflake) may change APIs; track and isolate per-engine code aggressively.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Oracle 11g (legacy) lacks modern VPD features                     | Document minimum supported versions; refuse older.                                               |
| Snowflake virtual warehouse suspended                              | Auto-resume; surface delay; account for it in cost gate.                                          |
| BigQuery dry-run cost mismatched with actual                      | Use dry-run + buffer; alert on > 20% deviation.                                                  |
| SQL Server Always Encrypted columns                               | Detect + skip masking (already encrypted); document.                                              |
| MongoDB aggregation pipeline with `$lookup` to a denied collection | Validator catches; rejects.                                                                       |
| MySQL temporary tables                                             | Per-session; ensure SET vars apply to the same connection.                                       |
| Snowflake masking-policy / row-access conflicts (limits per object) | Use shared policy objects; document.                                                              |
| BigQuery service-account impersonation latency                    | Cache impersonation tokens.                                                                       |
| Databricks Unity Catalog SCIM lag                                  | Map platform users → workspace users via daily sync.                                              |
| MongoDB schemaless docs with sensitive fields nested deeply       | Path-based ACLs; document depth limits.                                                          |
| Cross-engine policy authored once collides per engine             | Equivalence tests catch; admin console surfaces engine-specific deviation.                       |
| Proxy fails mid-streaming                                          | Retry on idempotent paginated APIs; abort on streaming engines with audit.                       |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Per-engine proxy fleets sized independently.
- BigQuery / Snowflake have per-tenant quotas at the platform layer.
- Connection pool patterns per engine; Snowflake reuses HTTP/2 connections.

### 7.2 Security
- Per-engine least-privilege service accounts.
- Per-engine native enforcement always-on as defense in depth.
- DDM where available (SQL Server, Snowflake) hardens against direct connection.
- BigQuery Policy Tags enforce at the storage layer.

### 7.3 Multi-Tenant Isolation
- Per-engine credentials per tenant data source.
- Per-engine context-propagation tested for cross-tenant containment.
- Snowflake / BigQuery account-level resource separation reserved for Enterprise tier.

### 7.4 Concurrency
- Per-engine connection pools.
- Per-engine cost gate threading.

### 7.5 Performance
- p99 proxy overhead < 20 ms for SQL engines.
- BigQuery / Snowflake REST proxy adds < 50 ms p99 (engine-side latency dominates).
- MongoDB proxy < 10 ms.

---

## 8. Recommended Improvements

### Architecture
- A clean `EnginePlugin` abstraction lets external contributors add engines.
- A shared `MaskFn` library transpiled per dialect.
- A `PolicyDialectAdapter` so the same `Policy` JSON renders correctly across engines.

### DX
- `engine-cli test --engine=mysql --policy=… --query=…` runs the rewrite + executes against an ephemeral container.
- Per-engine integration test scaffolds in CI.
- Per-engine demo seed scripts.

### UX
- Capability matrix surfaced in data source page.
- "Mock policy on this engine" preview in admin console.

### Reliability
- Per-engine circuit breakers.
- Auto-recovery for transient (warehouse resume, IAM lag).

### Observability
- Per-engine latency dashboards.
- Cost dashboards: BigQuery bytes, Snowflake credits, etc.
- Equivalence-test pass rate over time.

### Maintainability
- Per-engine docs in `docs/connectors/`.
- ADRs per engine: wire vs JDBC, native enforcement plan.
- Engine ownership matrix: who is the SME, who is the on-call.

---

## 9. Technical Considerations

### 9.1 DB Design
- Extend `data_sources` with `engine_capabilities jsonb` summarizing the engine's feature support.
- Per-engine connection state tables for sync state.

### 9.2 API Contracts
- Connector interface above.
- Per-engine admin endpoints for sync/diagnostics.

### 9.3 RBAC
- Per-engine permission requirements for the platform's service account documented.

### 9.4 Validation Flows
- Calcite per-dialect parsing/validation.
- Per-engine syntax sanity tests at sync time.
- Cross-dialect equivalence tests.

### 9.5 Caching
- Per-engine session-token cache.
- Per-engine schema metadata cache (Phase 7) with engine-aware staleness.

### 9.6 Queues & Background Jobs
- Native policy syncer per engine (hourly).
- Crawler scheduling per engine.

### 9.7 Audit Logs
- Per-engine query audit normalized to common schema.

### 9.8 Retry & Idempotency
- Idempotent at sync layer; transient retries with backoff.

### 9.9 Monitoring
- Per-engine SLOs.
- Per-engine cost dashboards + alarms.

### 9.10 CI/CD
- Ephemeral engine containers for SQL Server (Linux image), Oracle (XE), MySQL.
- Cloud-only engines (Snowflake/BigQuery) tested in `staging` against dedicated test accounts.
- Equivalence test gate.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                  | Likelihood | Impact   | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Per-engine quirks underestimated                                      | High       | High     | Budget per engine; specialist hire / contractor.                                                 |
| TDS / TNS wire complexity                                             | High       | Med      | JDBC bridge first; native wire later as optimization.                                            |
| Snowflake / BigQuery cost spikes                                      | Med        | High     | Pre-flight dry-runs + per-tenant cost gates.                                                     |
| MongoDB different semantics break policy parity                       | Med        | High     | MongoDB pipeline transformer; document deviations explicitly.                                    |
| Native enforcement object limits (Snowflake policy attach per table)  | Med        | Med      | Shared policy objects; documentation.                                                            |
| Vendor API changes (Snowflake, Databricks)                            | Med        | High     | Version pinning; vendor alert subscriptions; abstraction layer.                                  |
| Equivalence test flake                                                | Med        | Med      | Deterministic data fixtures; engine versions pinned in CI.                                       |

### Rollback
- Per-engine feature flag.
- Per-engine credential isolation enables targeted disable.
- Native-policy sync revert via maintained "previous version" snapshot.

### Future Extensibility
- Adding a new engine becomes a `Connector` plugin.
- Cross-engine federation via Trino as a future plug-in.
- Streaming engines (Kafka SQL, Flink SQL) reserved.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Connectors for all 7+ engines.
- [ ] Per-engine crawler.
- [ ] Per-engine proxy (wire or REST).
- [ ] Native last-line enforcement syncers.
- [ ] Calcite dialect coverage with required customizations.
- [ ] Equivalence test corpus.
- [ ] Per-engine docs + capability matrix.

### Acceptance Criteria
- [ ] All 7+ engines reachable through proxy in `staging`.
- [ ] A policy authored once enforces equivalently on ≥ 4 engines.
- [ ] Equivalence corpus passes for 50+ policies.
- [ ] Per-engine latency overhead within budgets.
- [ ] Native last-line enforcement in place; direct-connection users still constrained.

---

## 12. Production Readiness Checklist

- [ ] Per-engine on-call expertise identified.
- [ ] Per-engine cost dashboards + alarms.
- [ ] Per-engine DR runbook.
- [ ] Vendor change-notification subscriptions in place.
- [ ] Per-engine pen-test scenarios.
- [ ] Capability matrix documented + linked from admin UI.

---

## 13. Remaining Risks Carried Forward

- **Cross-engine federation** (one query → multiple engines) deferred.
- **Streaming engines** (Kafka SQL, Pulsar) not supported.
- **MongoDB AI authoring** may lag SQL engines.
- **Wave A wire protocols (TDS/TNS)** remain on JDBC bridges; performance optimization deferred.
- **Engine vendor API drift** managed by abstraction + monitoring; long-term cost.
