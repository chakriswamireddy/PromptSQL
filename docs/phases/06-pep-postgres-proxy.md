# Phase 6 — PEP v1: PostgreSQL Transparent Proxy + Calcite

> **Duration:** 10–14 weeks &nbsp; · &nbsp; **Owner:** Backend (DB internals + Go + Java) &nbsp; · &nbsp; **Dependencies:** Phases 0–5
> **Companion:** [`../implementation-plan.md` §Phase 6](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Stand up the **Policy Enforcement Point**: a transparent PostgreSQL wire-protocol proxy that rewrites every inbound SQL statement using PDP-derived row filters, column masks, and column allowlists before forwarding to the backend database. Real BI tools (Metabase, Tableau, Superset, psql, JDBC) point at the proxy with zero code changes and receive only the rows + columns + masked values they are permitted to see.

**Business rationale:** this is the *single hardest* phase but also where the product becomes *real*. A working proxy with sub-15ms p99 overhead converts the platform from "interesting architecture" into "shippable to design partners." Without it, the platform is only an authorization story; with it, the platform is a complete enforcement story.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Go-native PG wire protocol proxy (`jackc/pgproto3`) with SCRAM-SHA-256 auth.
- Java Calcite sidecar for parse / validate / rewrite / re-emit.
- PDP integration (`BulkDecide`).
- Connection pooling per `(tenant, data_source)` backed by PgBouncer.
- Connection-time token authentication.
- Query cost gate (`EXPLAIN` parse).
- Defense-in-depth: native PG RLS on managed DB mirroring proxy rules.
- Side-channel hardening: generic errors, system-table blocking.
- Audit emission per query.

**Out of scope**
- Other DB engines (Phase 11).
- AI-generated SQL (Phase 10).
- Step-up auth obligations execution (Phase 14).
- Risk-aware mid-flight cancellation (Phase 14).

**Ownership**
- **Drives:** Backend Lead (database internals expert).
- **Reviews:** Security (wire protocol, auth, side channels), Performance (latency budget), DBA (PgBouncer, pool sizing, RLS sync).
- **Strongly recommended:** hire or contract someone with shipped-Calcite experience.

---

## 3. Hard Dependencies & Sequencing

- Phase 3 PDP `BulkDecide` with row-filter AST emission.
- Phase 2 SessionContext.
- Phase 5 audit producer SDK.
- Phase 1 `data_sources` table + Vault-stored connection secrets.

Sequencing: wire protocol skeleton → SCRAM auth → connection-time token → Calcite parse/validate → rewrite rules → cost gate → audit → connection pooling → RLS syncer → load test → side-channel review.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 6.1 — Implementation Strategy

Three viable paths:

| Path                       | Pros                                          | Cons                                              |
| -------------------------- | --------------------------------------------- | ------------------------------------------------- |
| ProxySQL 3.x extension     | Mature wire impl; low effort                  | C++; hard to extend in this team's stack          |
| Go + `jackc/pgproto3`      | Native; easy to extend                        | Re-implement protocol bits; some complexity       |
| All-Java Calcite host      | Native Calcite                                | JVM ops; heavier resource footprint               |

**V1 recommendation:** **Go wire proxy + Java Calcite sidecar via gRPC**. Hot path Go for throughput; Calcite isolated.

### 6.2 — Wire Protocol Surface (V1)

Implement the minimum required:
- Startup, AuthenticationRequest, SASL (SCRAM-SHA-256), AuthenticationOK.
- Simple Query.
- Extended Query: Parse, Bind, Describe, Execute, Sync.
- ReadyForQuery, ErrorResponse, NoticeResponse.
- RowDescription, DataRow, CommandComplete.
- Terminate.

**Deferred to Phase 6.5 / Phase 11**: COPY, LISTEN/NOTIFY, large objects, prepared-statement caching beyond a single connection, replication protocol. Reject with explicit errors and audit metric `proxy_unsupported_command_total`.

### 6.3 — Calcite Sidecar

Java service `apps/proxy-calcite-sidecar`. gRPC API:

```proto
service CalciteRewriter {
  rpc Rewrite (RewriteRequest) returns (RewriteResponse);
}

message RewriteRequest {
  string raw_sql            = 1;
  string source_dialect     = 2; // 'postgres'
  string target_dialect     = 3; // 'postgres'
  Decision decision         = 4; // from PDP BulkDecide
  CatalogSnapshot catalog   = 5; // schema metadata for referenced tables
  map<string,string> binds  = 6;
  string tenant_id          = 7;
  string user_id            = 8;
}

message RewriteResponse {
  string rewritten_sql               = 1;
  repeated string referenced_tables  = 2;
  repeated string referenced_columns = 3;
  string ast_hash                    = 4;
  RewriteError error                 = 5;
}
```

Internals:
1. **Parse:** `SqlParser` with PostgreSQL `Config`.
2. **Validate:** catalog populated from `information_schema` snapshot per data source (Phase 7 keeps it fresh).
3. **Convert to `RelNode`** via `SqlToRelConverter`.
4. **Apply rewrite rules**:
   - For each referenced table, push an `AND` filter from `decision.row_filter[table]`.
   - Replace masked column references with `MASK_FN(col)` calls (Calcite UDF tables register `mask_email_domain`, `mask_credit_card`, etc.).
   - Strip denied columns from projections.
   - Enforce `LIMIT` (apply role/policy max).
5. **Convert back** via `RelToSqlConverter` + `PostgresqlSqlDialect`.

Performance:
- Parse + rewrite p99 ≤ 5 ms target for typical analytical queries.
- Pool of JVM workers; warmup on boot to avoid JIT cold-start.
- Caching: parsed `SqlNode` cache by `sha256(raw_sql)`; rewrite cache by `sha256(raw_sql + decision_hash)` (5-min TTL).

### 6.4 — End-to-End Query Flow

```
client BI tool
   ↓ PG wire (TLS)
proxy (Go)
   ├ accepts SCRAM auth; resolves SessionContext from password (connection token)
   ├ on Query (Simple/Extended):
   │   1. parse to detect statement type; reject non-SELECT (V1) or DDL/DML if not permitted
   │   2. quick deny list (banned keywords, system tables, COPY/LO)
   │   3. extract candidate tables via fast regex (refined by Calcite if uncertain)
   │   4. PDP.BulkDecide(tenant, user, action='read', resources=[table1, table2, …])
   │   5. CalciteRewriter.Rewrite(...) with merged decisions
   │   6. emit audit event (queued)
   │   7. acquire backend connection from (tenant, data_source) PgBouncer pool
   │   8. begin txn; SET LOCAL ROLE app_read; SET LOCAL app.user_id=…; tenant_id=…; campus_id=…; break_glass='false'
   │   9. execute rewritten SQL; stream rows back unchanged
   │  10. commit; release connection
   │  11. audit completion (row_count, duration_ms, query_hash)
   └ on connection close: cleanup; sweep orphans every 30 s
```

### 6.5 — Authentication Strategy

**V1 Option A — Connection-time token (default):**
- API Gateway `POST /v1/db-token` issues a short-lived (15 min) token tied to SessionContext.
- BI tool connects with username = real user, password = token.
- Proxy validates token (gRPC to api-gateway or via shared Redis lookup), constructs SessionContext.

**Phase 14 Option B — mTLS client certs:**
- Reserved for ML notebooks, CLI tools, machine identities; rolls in alongside step-up auth.

### 6.6 — Connection Pooling

- Per-`(tenant_id, data_source_id)` pool, backed by PgBouncer (transaction-pool mode).
- Pool sizing from tenant plan: Starter 25, Pro 100, Enterprise 500+ (dedicated PgBouncer per Enterprise tenant if isolation required).
- Backend connections re-used by serializing `SET LOCAL` discipline inside each transaction.
- Idle reaping: 5 min idle → close.
- Orphan sweeper kills backend connections whose proxy session has disappeared.

### 6.7 — Query Cost Gate

Before forwarding:
1. `EXPLAIN (FORMAT JSON) <rewritten_sql>` on the backend.
2. Parse `Total Cost`, `Plan Rows`.
3. Reject if exceeding role-derived caps (e.g., `analyst.maxCost=10000`, `maxRows=1_000_000`).
4. Backstops: backend-side `statement_timeout` and `idle_in_transaction_session_timeout` per role.

Cost gate adds ~2 ms; cache `EXPLAIN` result by `sha256(rewritten_sql)` 60 s for repeated analytical queries.

### 6.8 — Side-Channel Hardening

- Generic client errors: `permission denied for query` — no leakage of which column / row / policy.
- Specifics → audit + admin console only.
- Block direct access to `information_schema`, `pg_catalog`, `pg_*` tables.
- Block multi-statement queries (`;` body), `--` comments stripped + re-parsed.
- Block `pg_sleep` and other side-effecting functions.
- Result-set size cap (V1: 100k rows by default, role-overridable up to 10M).

### 6.9 — Defense-in-Depth: Native RLS Mirror

A "policy syncer" cron (`apps/proxy-rls-syncer`) ensures the backend PG has RLS policies that mirror proxy rules:
- For every active proxy policy on a table, create/update a PG RLS policy `proxy_mirror_<policy_id>`.
- Uses `current_setting('app.subject_role', true)` for differentiation.
- Hourly cron in V1; Phase 15 makes real-time via outbox + CDC.

If a customer (or attacker with leaked creds) bypasses the proxy and connects directly to the backend DB, native RLS still applies.

### 6.10 — Audit

Every accepted query emits:
- `query_hash = sha256(normalized_rewritten_sql + bindings_canonical)`
- `pdp_decision_summary` (matched policies, masks applied)
- `row_count`, `duration_ms`
- `rewrite_duration_ms`, `pdp_duration_ms` for performance attribution.

### 6.11 — Tests

- **Protocol conformance:** plug pgTAP-style tests against the proxy with psql, JDBC, pgx, asyncpg.
- **Correctness:** for 50 canonical queries, assert rewrite emits the expected predicates and projections.
- **Equivalence:** parse rewritten SQL with `sqlparse` and verify semantic equivalence with the original under PDP decisions.
- **Side-channel:** craft queries that try to leak via error messages or timing; verify uniform error + bounded timing.
- **Chaos:** kill backend connections mid-query; client sees clean error.
- **Load:** 1k concurrent connections, p99 overhead < 15 ms.

---

## 5. Architectural Gaps & Missing Requirements

1. **Prepared statement lifecycle.** Cross-query prepared statements with named bindings need careful handling. Define proxy behavior: re-parse on each Bind for safety; rely on Calcite plan cache for perf.
2. **Transactions spanning multiple queries.** A `BEGIN ... COMMIT` block with mid-block policy refresh — define snapshot semantics; recommended: lock policy_set_version for the duration of an explicit txn.
3. **Cursor / `FETCH` semantics.** Streaming cursors need backpressure; document cap.
4. **`COPY` semantics.** Reject in V1; reserve future support with mask-aware row emission.
5. **JSON / JSONB column masking.** Mask at sub-key level requires custom rewrite rule; document the V1 capability matrix.
6. **Time-window-dependent policies (e.g., date-range obligations).** Cache invalidation must consider time-bound predicates; design `decision.ttl` accordingly.
7. **Per-data-source dialect quirks.** Even within Postgres, AWS Aurora, Citus, Timescale, AlloyDB have quirks. Maintain a compatibility matrix.
8. **Pool starvation under noisy tenant.** Per-tenant pool maxes + fairness scheduling; document SLO.
9. **Backend version skew.** PG 12 vs 17: function availability differs. Auto-detect server version on first connect; refuse to rewrite using functions unavailable on backend.
10. **Audit-on-failure semantics.** Failed (rejected) queries also audit; never skip.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Calcite sidecar crash                                             | Per-request circuit breaker; on outage refuse new queries with clear error.                      |
| PDP outage                                                        | Fail closed: deny new queries; cached decisions usable for in-flight txns within TTL.            |
| Backend DB connection storm                                       | Backoff with jitter; per-pool circuit breaker; alarm on connection-error rate.                   |
| Client driver sends pipelined queries (Extended)                  | Buffer parses + binds; rewrite each on Execute; never re-order.                                  |
| Long-running query exceeds `statement_timeout`                    | Backend cancels; proxy returns clean error and audits.                                            |
| Client disconnects mid-stream                                     | Proxy issues `CancelRequest` to backend; idle connection reaped.                                  |
| Backend RLS misconfigured (no mirror policy)                      | Syncer fail; admin console banner; new queries denied to data source.                            |
| Mask UDF missing on backend                                       | Detected during pre-flight; syncer creates `mask_*` SQL UDFs on managed DB.                       |
| User holds two sessions, second refreshes; first uses stale decisions | Decision TTL ≤ MFA window; per-session pinning.                                              |
| Concurrent policy activation during in-flight query               | Snapshot policy_set_version at query start; in-flight uses snapshot; next query picks up new.    |
| Adversarial SQL: nested CTEs with side-channel timing             | LIMIT enforcement + cost gate + uniform error message.                                            |
| `EXPLAIN` itself denied by backend RLS                            | Backend role allows EXPLAIN against virtual catalog only; proxy generates EXPLAIN as `app_read`. |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Horizontal scaling: each proxy node is stateless beyond per-connection state.
- 1 proxy core ≈ 2k connections sustained with light traffic.
- Sidecar JVM sized per worker; HPA on rewrite latency, not CPU alone.

### 7.2 Security
- TLS 1.3 client side; TLS 1.3 + SCRAM backend side; never plaintext.
- Connection tokens never logged.
- Reject `SET ROLE` from clients (only proxy uses SET LOCAL ROLE).
- Reject custom GUCs that begin with `app.` from client side.
- Strict regex denylist on `pg_*`, `information_schema.*`.
- Backend role `app_read` has no superuser, no `pg_read_server_files`, no `lo_*` functions, no `COPY ... FROM PROGRAM`.

### 7.3 Multi-Tenant Isolation
- Per-tenant pool isolation prevents noisy-neighbor.
- Backend RLS as last-line defense.
- Audit + cost gate per tenant; budget alerts.

### 7.4 Concurrency
- Per-connection state in goroutines; channels for backpressure.
- gRPC streaming for Calcite when batch-rewrite is needed.
- Lock-free atomic counters for hot stats.

### 7.5 Performance
- p99 proxy overhead (proxy in - backend in) < 15 ms.
- Calcite rewrite p99 < 5 ms (warm cache).
- PDP BulkDecide p99 < 5 ms (cached).
- Cost gate p99 < 3 ms.
- Total user-perceived overhead p99 ≤ 15 ms.

---

## 8. Recommended Improvements

### Architecture
- **Calcite-as-library** in a second Go-callable JNI bridge as a later optimization to remove sidecar hop; sidecar V1 keeps the team safe.
- **`PolicyBundle`** distributed to proxy nodes via Phase 3 outbox → enables offline rewriting if PDP momentarily unavailable.

### DX
- A `proxy-cli rewrite --policy-bundle=…` for offline testing of rewrites.
- Trace-replay tooling that replays a captured session for debugging.
- Local devs can `psql 'host=localhost port=5450'` and see audited behavior identical to prod.

### UX
- Admin console: per-query inspector showing original SQL, rewritten SQL, decisions consulted, masks applied, audit row.
- BI tool connection wizard generates token + connection string.

### Reliability
- Connection draining on rolling deploy: stop accepting new connections, finish in-flight, then exit.
- Health probe distinguishes liveness vs. readiness (no sidecar = not ready).

### Observability
- Per-query OTel span links: client conn → proxy parse → PDP call → Calcite call → backend exec.
- Metrics: `proxy_rewrite_duration_ms`, `proxy_pdp_duration_ms`, `proxy_backend_duration_ms`, `proxy_rows_streamed_total`, `proxy_rows_masked_total`.
- Dashboards: queries per tenant per data source, rejection rate, cost-gate trips.

### Maintainability
- ADRs: Go-vs-all-Java, sidecar pattern, mask UDF naming, RLS mirror cadence.
- Calcite version pin + scheduled upgrade quarterly.
- Strict separation of wire-protocol parsing (Go) from SQL semantics (Calcite).

---

## 9. Technical Considerations

### 9.1 DB Design
- New PG tables: `proxy_session_tokens` (Redis-backed primarily; PG mirror for forensics), `rls_sync_state(data_source_id, last_synced_at, status)`.
- Backend managed DB: `mask_*` SQL UDFs installed by syncer; RLS policies named `proxy_mirror_<policy_id>`.

### 9.2 API Contracts
- gRPC: `CalciteRewriter`, `PDP.BulkDecide` (existing).
- HTTP: `/admin/proxy/sessions/{id}` (admin debug), `/admin/proxy/rewrite-preview` (preview a SQL for a user).

### 9.3 RBAC
- Backend DB roles minimized; proxy is the only entity with `app_read` access.
- Per-pool role enforcement: pool's underlying user can `SET LOCAL ROLE` to a constrained set only.

### 9.4 Validation Flows
- Rewrite-time validation against catalog snapshot.
- Backend-version validation at startup; refuse to launch if version below minimum.
- Mask UDF presence validated at startup per data source.

### 9.5 Caching
- Parsed `SqlNode` cache per Calcite worker.
- Rewrite cache (raw_sql + decision_hash) 5 min.
- `EXPLAIN` cost cache 60 s.
- Connection-token cache 15 min in Redis.

### 9.6 Queues & Background Jobs
- RLS syncer hourly.
- Orphan connection sweeper every 30 s.
- EXPLAIN cache warmer for top-N queries per tenant.

### 9.7 Audit Logs
Every query (accept or reject), every rewrite, every cost-gate trip — Phase 5 producer SDK.

### 9.8 Retry & Idempotency
- PDP calls retry on transient errors; idempotent.
- Calcite calls retry once; second failure → fail-closed.
- Backend retries off (let the application handle).

### 9.9 Monitoring
SLOs:
- 99.9% queries rewritten within budget.
- 99.95% successful queries match PDP decision (no false denies).
Alerts: rewrite error rate > 0.1%, RLS syncer fail, cost-gate trip rate spike.

### 9.10 CI/CD
- Wire-protocol conformance suite per release.
- Cross-client matrix: psql, pgx, JDBC, asyncpg, ODBC.
- Load test in `staging` weekly.
- Canary deploys to a single tenant first; promote on 24 h green.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                              | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Calcite learning curve underestimated                             | High       | High     | Hire / contract; budget 3 weeks for sidecar.                                                     |
| PG protocol edge cases (COPY, LO, replication)                    | High       | Med      | Reject unsupported in V1 with clear errors + audit; ship matrix; incrementally support.          |
| Connection-pool meltdown under leaky client                       | Med        | High     | Orphan sweeper + per-pool cap + tenant quotas; tested under load.                                |
| Side-channel leakage via error messages or timing                 | Med        | Critical | Uniform errors; bounded timing; pen-test scope.                                                  |
| RLS syncer drifts from policies                                   | Med        | High     | Verifier diffs proxy policy set vs. native RLS; alarm.                                            |
| Rewrite incorrectness (semantic change)                           | Med        | Critical | Equivalence test corpus; canary deploy; admin console preview.                                   |
| Calcite parses but produces non-equivalent SQL                    | Low        | Critical | Same equivalence tests; shadow mode runs both original (in a sandbox) and rewritten on a sample. |
| Backend version skew breaks rewrite                               | Med        | High     | Per-version compatibility check + dialect option.                                                 |
| Customer drivers behave outside spec                              | High       | Med      | Per-driver conformance matrix + workarounds.                                                     |

### Rollback
- Per-tenant kill switch routes traffic back to direct PG (bypass proxy) with audit alert.
- Per-version rollback for sidecar + proxy via Helm.
- Decision cache invalidation safe on rollback (versioned keys).

### Future Extensibility
- Phase 11 brings other DBs; sidecar abstraction (different `SqlDialect`) is the right seam.
- Phase 14 introduces obligations → step-up MFA returned to client via `ErrorResponse` with structured payload.
- Phase 13 risk-aware mid-flight masking integrates at row streaming layer.
- WebAssembly UDFs as a future direction for masking, deferred.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Go wire proxy + Java Calcite sidecar in `dev`, `staging`.
- [ ] Connection-token issuance + verification.
- [ ] PDP `BulkDecide` integration.
- [ ] Calcite rewrite rules: row filter, column mask, column allowlist, LIMIT injection.
- [ ] Cost gate.
- [ ] PgBouncer-backed connection pooling per (tenant, data source).
- [ ] Native RLS mirror syncer (hourly).
- [ ] Mask UDF installer.
- [ ] Side-channel hardening: generic errors, system-table blocking, multi-statement reject.
- [ ] Per-query audit emission.
- [ ] Wire-protocol conformance suite, equivalence corpus, load test.

### Acceptance Criteria
- [ ] psql, pgx, JDBC clients all connect and execute SELECTs via the proxy.
- [ ] Without policy → query fails with `permission denied for query` (no leakage).
- [ ] With policy → query is rewritten, masks applied, results streamed.
- [ ] p99 proxy overhead < 15 ms vs direct connection.
- [ ] Audit event per query.
- [ ] Direct connection to backend (bypassing proxy) enforces RLS mirror.
- [ ] Equivalence corpus passes; no false denies; no false permits.

---

## 12. Production Readiness Checklist

- [ ] Connection draining tested on rolling deploy.
- [ ] DR runbook: Calcite sidecar restart, PDP outage, backend cluster failover.
- [ ] Per-tenant pool sizes documented + alerts wired.
- [ ] Pen-test scope includes wire protocol, error leakage, timing.
- [ ] Cost-gate caps documented; admin UI surfaces caps per role.
- [ ] Backend role grants minimized + audited.
- [ ] Mask UDF rollout runbook.
- [ ] Synthetic transaction probes for top-N customer queries.

---

## 13. Remaining Risks Carried Forward

- **Only PostgreSQL** until Phase 11; multi-engine support is the next budgeted bulge.
- **No mid-flight cancellation** until Phase 14; risk spikes during a streaming query are not yet acted on.
- **JSON/JSONB sub-key masking** limited; full support reserved for later.
- **`COPY` / replication** unsupported; document.
- **mTLS client cert auth** deferred to Phase 14.
- **Backend RLS sync** hourly in V1; CDC-based realtime in Phase 15.
- **Calcite sidecar hop** is a latency overhead the team accepts in V1; JNI bridge optimization is a future ADR.
