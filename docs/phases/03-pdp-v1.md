# Phase 3 — Policy Decision Point (PDP) v1

> **Duration:** 5–7 weeks &nbsp; · &nbsp; **Owner:** Backend (Go, authorization) &nbsp; · &nbsp; **Dependencies:** Phases 0–2
> **Companion:** [`../implementation-plan.md` §Phase 3](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Deliver a centralized, deterministic, sub-5ms-p99 authorization engine that evaluates `(subject, action, resource, context) → Decision` using policies stored in the control plane. Every gateway service, the proxy (Phase 6), the AI orchestrator (Phase 9–10), and the admin simulator (Phase 4) call the PDP — there is **exactly one** place authorization logic lives.

**Business rationale:** authorization scattered across services is the #1 cause of inconsistent enforcement and audit failures. A single PDP with a bounded DSL, deny-overrides semantics, and a tight cache makes every policy decision reproducible, auditable, and SOC 2-friendly. Determinism + reproducibility = customer trust + incident forensics.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Standalone Go service `apps/pdp`.
- gRPC `Decide`, `BulkDecide`, HTTP debug `/decide`, `/explain`.
- Conditions DSL: parser, validator, AST, compiler-to-closure, SQL emitter.
- Deny-overrides decision algorithm with column/row/obligation merging.
- Two-tier cache (in-process L1, Redis L2), single-flight, versioned keys.
- Pub/sub invalidation on policy mutations.
- Comprehensive unit, property, fuzz, benchmark tests.

**Out of scope**
- Risk score input (Phase 13).
- Break-glass overrides at runtime (Phase 14).
- Per-DB SQL dialect rewriting (Phase 6 / Calcite).
- Adversarial reviewer / policy mining (Phase 9+).

**Ownership**
- **Drives:** Backend Lead (Go authorization specialist).
- **Reviews:** Security (DSL boundedness, deny semantics), Performance (benchmarks).
- **Hand-off:** Phase 4 admin console + Phase 6 proxy consume the gRPC API.

---

## 3. Hard Dependencies & Sequencing

- Phase 1: `policies`, `data_classifications` tables.
- Phase 2: `SessionContext` shape and propagation.
- Phase 0: Redis cluster (dev compose), OTel, feature flags.

Sequencing: DSL → AST/validator/compiler → decision algorithm (pure function) → cache → service shell → pub/sub → tests → harden.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 3.1 — Service Shape

`apps/pdp` (Go, fiber/grpc-go). Endpoints:

| RPC / HTTP                | Purpose                                           |
| ------------------------- | ------------------------------------------------- |
| `Decide` (gRPC)           | Single decision, lowest-latency path              |
| `BulkDecide` (gRPC)       | N decisions in one round-trip (used by proxy)     |
| `Explain` (gRPC + HTTP)   | Returns decision + matched rules + obligations    |
| `Validate` (gRPC + HTTP)  | Validates a draft policy without persisting      |
| `/healthz`, `/readyz`     | Standard                                          |
| `/metrics`                | Prometheus                                        |

`Decide` request includes serialized `SessionContext`, `action`, `resource` (URI-like: `pg://datasource/schema/table/column`), and optional `context` map (e.g., row attributes when known).

### 3.2 — Conditions DSL

A **bounded, decidable** JSON DSL. Grammar (informal):

```
Condition  := { "all": [Condition...] }
            | { "any": [Condition...] }
            | { "not": Condition }
            | Predicate

Predicate  := { "field": Path, "op": Op, "value": Literal }
            | { "field": Path, "op": "in", "value": [Literal...] }
            | { "field": Path, "op": "between", "value": [Literal, Literal] }

Path       := "subject.<attr>" | "resource.<attr>" | "context.<key>" | "env.<key>"
Op         := eq | neq | lt | lte | gt | gte | in | nin | between | startsWith | endsWith | contains | matches(regex, anchored)
Literal    := string | number | bool | iso-date
```

Constraints:
- **Max depth 5.**
- **Max nodes 256.**
- **No backreferences, no loops, no function calls** outside the operator set.
- **Regex** runs RE2 (no catastrophic backtracking); 256-char input cap.
- **Date comparisons** ISO-8601 only; timezone explicit.
- **No string interpolation**; values are literals, never templated.

**Alternative:** adopt **CEL** (Common Expression Language, Google) and constrain it. CEL has Go bindings, mature tooling, and is provably decidable. *Recommendation*: bespoke DSL for V1 because we control its evolution; CEL evaluated as a Phase 11 swap. ADR documents both choices.

### 3.3 — DSL Components

1. **Parser** — JSONB → typed Go AST.
2. **Validator** — depth, node count, operator allowlist, path resolution, type compatibility, tenant containment (`subject.tenantId` always equals current tenant).
3. **Compiler-to-closure** — AST → `func(SessionContext, ResourceAttrs, ContextMap) bool`. Single allocation per call; no reflection on hot path.
4. **SQL emitter** — AST → dialect-neutral predicate (Calcite RexNode-compatible JSON) — consumed by Phase 6 proxy.
5. **Explainability** — emitter that produces a human-readable trace ("matched policy P, condition `subject.department == 'finance'` evaluated true via attributes from SessionContext").

Closures are **pre-compiled at policy activation time** and held in process memory keyed by `policy_id`. Recompiled on invalidation pub/sub.

### 3.4 — Decision Algorithm (Deny-Overrides)

Pure function. Input: list of matching policies + `SessionContext`. Output: `Decision`.

```
1. Gather candidate policies: tenant_id match, action match, resource match (resource is a tree:
   db → schema → table → column; a policy at any level matches its subtree).
2. Filter by `conditions(SessionContext)` true.
3. If any candidate has `effect = "deny"` → DENY; record matched rule IDs.
4. Else if any candidate has `effect = "allow"`:
     allowedColumns = ⋃ allow.allowed_columns ⊖ ⋃ deny.denied_columns
     rowFilter      = AND(allow.row_filter for each matched allow)   // boolean AND
     columnMasks    = merge(allow.column_masks)                      // last-wins by column, or merge function
     obligations    = ⋃ allow.obligations  ⋃  ⋃ deny.obligations
     For each obligation, evaluate satisfiability now (e.g., mfa_at within window).
     If any obligation unsatisfiable AND non-deferrable → DENY.
     Else → PERMIT with merged row filter, columns, masks, obligations.
5. Else → DENY (default).
```

**Properties to test:**
- Default deny.
- Monotonicity: removing an allow never grants more, removing a deny never restricts more.
- Determinism: same input always same output.
- Tenant containment: candidate filter ensures cross-tenant always 0 candidates.
- Idempotency: re-applying a decision computation is hash-stable.

### 3.5 — Caching: Two-Tier

| Layer | Storage                  | Hot-path latency | Invalidation                |
| ----- | ------------------------ | ---------------- | --------------------------- |
| L1    | sync.Map + LRU 10k       | < 100 ns         | pub/sub event + version key |
| L2    | Redis (cluster in prod)  | < 1 ms           | pub/sub event + version key |

**Cache key:** `pdp:v1:{tenantId}:{userId}:{action}:{resourceSHA1}:{policyVersion}:{attrsSHA1}`

`policyVersion` is a per-tenant monotonic counter (`policy_set_version`), bumped on every policy mutation. Stale entries become unreachable rather than served. `attrsSHA1` hashes the *normalized* attributes that participated in evaluation (subject + resource + context).

**Stampede protection:** single-flight per cache key (Go `singleflight.Group`).

**Negative caching:** denies are cached too, with same TTL; explicit "no policy" decisions cached as defaults.

### 3.6 — Pub/Sub Invalidation

- Phase 4 admin write completes → publishes `policy.invalidate.{tenantId}` on Redis pub/sub with `{policy_set_version, policy_ids}`.
- Each PDP node subscribes; on receipt:
  1. Increments local `policy_set_version[tenantId]`.
  2. Drops affected L1 entries.
  3. Recompiles closures for changed policies (pulled from PG).
  4. Publishes ACK on `policy.ack.{nodeId}` for an admin diagnostic UI.
- Stale-detection fallback: every 30s each node polls `SELECT max(version) FROM policies WHERE tenant_id` for tenants with active queries; mismatch triggers refresh (defends against pub/sub outage).

### 3.7 — gRPC Contract (proto v1)

```proto
service PDP {
  rpc Decide      (DecideRequest)      returns (Decision);
  rpc BulkDecide  (BulkDecideRequest)  returns (BulkDecideResponse);
  rpc Explain     (DecideRequest)      returns (DecisionExplanation);
  rpc Validate    (ValidateRequest)    returns (ValidateResponse);
}

message DecideRequest {
  bytes  subject_session_context = 1; // serialized + HMAC-signed
  string action                  = 2;
  string resource                = 3; // URI form
  string data_source_id          = 4;
  map<string,string> context     = 5;
  string idempotency_key         = 6;
}

message Decision {
  Effect effect                            = 1; // PERMIT | DENY
  repeated string allowed_columns          = 2;
  repeated string denied_columns           = 3;
  map<string,string> column_masks          = 4;
  RowFilter row_filter                     = 5; // AST JSON
  repeated Obligation obligations          = 6;
  string reason                            = 7;
  string policy_set_version                = 8;
  int32  ttl_seconds                       = 9;
  repeated string matched_policy_ids       = 10;
}
```

Version every message with a `v1` package; breaking changes ship as `v2` alongside.

### 3.8 — Tests (Non-Negotiable)

- **Unit:** ~200 hand-crafted policy sets covering every combination of allow/deny/columns/rows/obligations.
- **Property** (via `gopter`/`rapid`):
  - Monotonicity, determinism, tenant containment, ordering invariance.
  - Adding a deny never permits; adding an allow never denies overall when a deny exists.
- **Fuzz** (Go native fuzz): malformed JSON, deeply nested, oversized strings, Unicode chaos, integer overflow, malformed regex.
- **Bench:**
  - p99 < 5 ms at 10k RPS per node with 90/10 miss-ratio.
  - L1 hit p99 < 100 µs.
  - Compile-time of a 100-policy tenant < 50 ms.
- **Chaos:** kill node mid-invalidation; verify state recovery from `policy_set_version`.

### 3.9 — Observability

OTel attributes per `Decide` span:
- `pdp.tenant_id`, `pdp.user_id`, `pdp.action`, `pdp.resource`
- `pdp.effect`, `pdp.matched_policy_ids[]`
- `pdp.cache_layer` ∈ `{L1, L2, miss}`
- `pdp.policy_set_version`
- `pdp.compile_age_ms`

Metrics:
- `pdp_decision_total{effect, cache, tenant}`
- `pdp_decision_duration_seconds{cache}`
- `pdp_compile_duration_seconds`
- `pdp_invalidate_total{result}`
- `pdp_active_policies_total{tenant}`

---

## 5. Architectural Gaps & Missing Requirements

1. **Resource taxonomy formalization.** Resource URIs need a canonical grammar (`<kind>://<datasource>/<schema>/<table>[/<column>]`). Document and validate.
2. **Resource attribute resolution.** Some policies need resource attrs (e.g., `resource.classification`); these come from `data_classifications`. PDP must cache them per `(resource, tenant)`.
3. **Subject attribute resolution staleness.** Phase 2 resolves roles at gateway, but the PDP also pulls `data_classifications` and other resource attrs on its own. Define a single canonical pipeline.
4. **Decision TTL semantics.** Cache TTL vs. obligation expiry (e.g., MFA-recency). Decision TTL ≤ min(role-cache TTL, MFA-window).
5. **Policy precedence (allow priorities).** Pure deny-overrides is correct but customers may want priorities. Reserve a `policies.priority int` column for future use; V1 ignores it.
6. **Resource hierarchy semantics.** Does an allow on a `table` imply allow on its columns? Document explicitly: yes by default, but a column-level deny overrides.
7. **Multi-resource decisions (one query → many tables).** `BulkDecide` is the answer; spec the batching contract precisely.
8. **Negative obligations** (e.g., "mask if condition X"). Are masks obligations or fields? V1 keeps them as `column_masks` map; revisit in Phase 11.
9. **Drift detection.** What happens when a policy references a column that has since been dropped? Validator must flag at activation; PDP fails the closure compile.
10. **PDP-to-PDP consistency.** Different PDP nodes may see slightly different cache states; document the eventual-consistency window and SLA.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                              | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Policy references a column not in `schema_metadata`                  | Validator rejects at activation; PDP closure compile fails, alerts steward.                      |
| Tenant has 100k policies                                              | Index `policies(tenant_id, status, version, action)`; lazy-load per-action subset; benchmark.    |
| Conflicting allow row-filters (e.g., `region=us` AND `region=eu`)    | AND combination yields empty set; correct semantically, but warn in simulator.                   |
| Obligation `require_mfa_within=5min` and user MFA'd 6 min ago        | Demote to deny with reason `mfa_stale`; surface to client as 401 step-up.                        |
| Redis outage                                                          | L1 absorbs; on full miss, PG fallback per request; circuit breaker; metric `pdp_redis_down`.    |
| Pub/sub message lost                                                  | 30s poller covers; explicit `force-recompile` admin endpoint.                                    |
| Closure compile panic on malformed AST                                | Validator must catch; runtime defensive recover + emit `pdp_compile_panic_total`.                |
| Cross-tenant context attack via spoofed `SessionContext`             | HMAC verification (Phase 2.5) rejects; gateway is the only minting authority.                   |
| `BulkDecide` partial failure                                          | Per-item status; never fail the whole batch.                                                     |
| Cache key collision (hash truncation)                                 | Use SHA-256 truncated to 16 bytes; document collision risk negligible.                           |
| Clock skew skews obligation evaluation                                | Use authoritative server time; NTP alarms in Phase 0.                                            |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Stateless service; horizontally scaled by CPU.
- Cache memory dominates per-pod RAM; cap L1 entries; eviction LRU.
- Per-tenant fairness: noisy tenant cannot starve neighbors — token bucket per `tenantId` on the request path.

### 7.2 Security
- Verifies HMAC on inbound `SessionContext`.
- Refuses to evaluate cross-tenant decisions.
- DSL is bounded — no Turing-completeness, no resource-exhaustion vectors.
- Defends against ReDoS via RE2 + input length limits.
- Time-constant comparisons where applicable.

### 7.3 Multi-Tenant Isolation
- Per-tenant `policy_set_version` keyed cache.
- Per-tenant token-bucket throttling.
- Per-tenant compile budget.

### 7.4 Concurrency
- Compile pipeline is goroutine-safe; `sync.Map` for closures.
- Pub/sub handler serialized per tenant to avoid version regression.
- Single-flight on cache miss.

### 7.5 Performance
- p99 < 5 ms with cache; p99 < 25 ms cold.
- Compile budget: < 50 ms per 100-policy tenant.
- Memory budget per pod: < 1 GB for 1k tenants average.

---

## 8. Recommended Improvements

### Architecture
- Treat the **decision algorithm** as a library (`packages/policy-engine` in Go) so the admin simulator (Phase 4) imports the same code path — zero divergence between simulator and runtime.
- A **policy bundle** abstraction: an immutable snapshot of all active policies for a tenant + version, distributed as a single object so PDP nodes can hot-swap atomically.

### DX
- A CLI `pdp-cli evaluate --policy=… --subject=… --resource=…` for offline debugging.
- A `pdp-cli simulate` that mirrors the admin simulator.
- Generated OpenAPI for `/explain` makes it easy for support engineers to debug decisions in a browser.

### UX (downstream Phase 4)
- `Explain` returns structured *and* human-readable reason strings: "Allow: policy P-123 matched because subject.department == 'finance' and resource.classification == 'internal'."
- A "why was I denied?" surface that maps deny reasons to remediation suggestions (request access, missing MFA, classification, etc.).

### Reliability
- Stable-when-stuck behavior: if pub/sub stops, the 30s poller maintains eventual consistency.
- Bulkheads: separate worker pools for L2 lookups, compile, and PG fetch.
- Circuit breakers around Redis + PG.

### Observability
- Trace exemplars: link cache-miss spans to PG query traces.
- Decision-distribution dashboards (effect, masked-column counts, row-filter applied) per tenant.
- Compile-error dashboard with last-N failing policies.

### Maintainability
- Property-test suite gated in PR; failures are blocking.
- Decision-algorithm changes require an ADR + property-test additions.
- ADRs for: DSL grammar, cache key design, deny-overrides choice.

---

## 9. Technical Considerations

### 9.1 DB Design
- No new tables in Phase 3; reads `policies`, `data_classifications`, `roles`, `user_roles`.
- Add `policy_set_versions(tenant_id, version, created_at)` for audit.

### 9.2 API Contracts
- gRPC v1 proto frozen; v2 alongside for breaking changes.
- HTTP `/explain` mirrors gRPC for debugging.

### 9.3 RBAC
- PDP itself authenticates inbound callers via mTLS / HMAC.
- Only `admin-console`, `proxy`, `ai-orchestrator`, `api-gateway` allowed to call `Decide`; per-caller scopes restrict `Validate` to admin-console.

### 9.4 Validation Flows
- DSL validator runs at policy activation in admin console (synchronous) AND at PDP startup (defensive).
- `Validate` endpoint provides dry-run validation for admins authoring drafts.

### 9.5 Caching
Covered above. Bears repeating: **versioned keys make staleness physically impossible** when invalidation pub/sub works; the 30s poller is belt-and-suspenders.

### 9.6 Queues & Background Jobs
- Compile worker: a worker pool that consumes activation events and pre-compiles closures.
- Cache warmer: on PDP cold start, warms common subjects for each tenant.

### 9.7 Audit Logs
- Every `Decide` produces a candidate audit event (forwarded to Phase 5 pipeline). Sampling: 100% in `dev`, configurable in `prod` (default 100% for denies, 10% for permits to control volume).
- `Validate` requests audited (admin authoring).

### 9.8 Retry & Idempotency
- `Decide` idempotent by definition; clients may include `idempotency_key` for tracing.
- `Validate` mutates nothing.

### 9.9 Monitoring
- Alerts: p99 > 25 ms / 5 min, cache miss rate > 50% / 5 min, compile errors > 0, decision-explanation queue depth growing.

### 9.10 CI/CD
- Bench-regression check: p99 must not regress > 20%.
- Property tests run on every PR.
- Proto-breaking-change check.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                    | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Over-clever DSL grows toward Turing-complete                            | High       | Critical | DSL is *bounded*; ADR documents constraints; CI grammar test.                                    |
| Cache invalidation bug serves stale permit                              | Med        | Critical | Versioned keys + 30s poller + chaos test.                                                        |
| ReDoS via crafted policy regex                                          | Low        | High     | RE2 + 256-char cap + test corpus of malicious patterns.                                          |
| Compile-error policy blocks tenant entirely                             | Low        | High     | Compile failure ≠ deny-all; the *broken policy* is dropped, others continue; alert steward.      |
| Stampede on cache miss                                                  | Med        | Med      | Single-flight per key.                                                                           |
| Misalignment between PDP simulator and runtime                          | Med        | High     | Share `packages/policy-engine` library; CI runs both against identical fixtures.                 |
| `policy_set_version` race in multi-region                               | Low        | High     | Single-writer in V1; multi-region writes deferred to Phase 15 with strict serializable writes.   |

### Rollback
- Feature-flag per caller (proxy, admin-console, AI orchestrator) routes to PDP or to a legacy passthrough during rollout.
- Decision algorithm changes ship behind a flag; A/B compare decisions in shadow mode for 7 days before flip.
- Schema-level rollback: forward-only; if a new field breaks deserialization, ship a fix-forward.

### Future Extensibility
- **CEL integration**: a `policies.lang` column reserves space for `cel`/`json-dsl`; engine dispatches by tag.
- **Policy bundles**: package + sign + distribute as artifacts (think OCI artifacts in Phase 15).
- **Bedrock for resource attrs**: a future `resource_attrs` service supplies row-level attrs (Phase 13 ties risk in).
- **Multi-language SDK** (Go / TS / Python) lets customers embed the PDP in their own services.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] `apps/pdp` service deployed to `dev` and `staging`.
- [ ] DSL: parser, validator, compiler-to-closure, SQL emitter.
- [ ] Deny-overrides algorithm in `packages/policy-engine`.
- [ ] Two-tier cache + pub/sub invalidation + 30s poller fallback.
- [ ] gRPC + HTTP APIs; OpenAPI for `/explain`.
- [ ] Property + fuzz + bench suites in CI.
- [ ] OTel + Prometheus dashboards.

### Acceptance Criteria
- [ ] Benchmarks: p99 < 5 ms at 10k RPS per node.
- [ ] Property tests pass; monotonicity, determinism, tenant containment, ordering invariance.
- [ ] Pub/sub invalidation propagates to all PDP nodes in < 100 ms.
- [ ] Removing a role from a user reflects in PDP within 1 s (sensitive grants) / 60 s (general).
- [ ] Chaos test: kill node mid-invalidation; state restored from `policy_set_version` on restart.
- [ ] Simulator (Phase 4) and PDP runtime produce identical decisions on a 1000-case corpus.

---

## 12. Production Readiness Checklist

- [ ] Per-tenant rate limits + alerting.
- [ ] mTLS or HMAC verified on every inbound call.
- [ ] DR runbook: PDP cold start, cache rebuild, compile-failure recovery.
- [ ] Capacity model documented (CPU, memory, Redis IOPS).
- [ ] Pen-test scenarios: DSL injection, regex DoS, version skew, cross-tenant probe.
- [ ] Decision-shadow-mode flag exists for safe rollout of algorithm changes.

---

## 13. Remaining Risks Carried Forward

- **Risk score not yet a variable** — Phase 13 will inject `subject.riskScore`; PDP must accept it without redeploy (via versioned schema).
- **Break-glass semantics** — Phase 14 introduces a deny-bypass path; PDP must honor `app.break_glass` carefully without short-circuiting audit.
- **Resource attribute service** — row-level attrs are inlined in `context` map today; a dedicated PIP (Policy Information Point) is deferred.
- **Adversarial review** — Phase 9 introduces; until then, policies depend on human review quality.
- **Cross-region consistency** — single-writer in V1; multi-master deferred to Phase 15.
