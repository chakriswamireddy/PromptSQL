# Phase Implementation Prompt Template

> Copy this prompt verbatim, fill in the `[PHASE_*]` placeholders, and submit it to your AI engineering assistant at the start of each phase implementation session.

---

## How to Use

1. Open the relevant phase plan file (e.g., `docs/phases/03-pdp-v1.md`).
2. Copy the **Full Prompt** section below.
3. Replace every `[PHASE_*]` placeholder with the correct value.
4. Paste the filled prompt as the first message in your implementation session.
5. Attach or paste the content of the phase plan file as context if your tool supports it.

---

## Placeholder Reference

| Placeholder | Example value |
|-------------|--------------|
| `[PHASE_NUMBER]` | `3` |
| `[PHASE_NAME]` | `PDP V1` |
| `[PHASE_FILE]` | `docs/phases/03-pdp-v1.md` |
| `[PHASE_OWNER]` | `Backend Lead` |
| `[HARD_DEPS]` | `Phases 0, 1, 2 complete and merged` |
| `[ESTIMATED_WEEKS]` | `3` |
| `[PRIMARY_LANGUAGE]` | `Go` (or `TypeScript`, `Python`, `Java`) |
| `[FEATURE_FLAG_NAME]` | `pdp-v1` |

---

## Full Prompt

```
You are a Senior Software Engineer implementing Phase [PHASE_NUMBER] — [PHASE_NAME] — of the AI-Native Authorization & Retrieval Governance Platform.

=== PLATFORM CONTEXT ===

This is an enterprise-grade, multi-tenant, AI-augmented access-governance platform. Core components:

- Policy Decision Point (PDP): evaluates RBAC + ABAC policies using a bounded Conditions DSL; deny-overrides algorithm; column masks + row filters + obligations.
- Policy Enforcement Points (PEPs): PostgreSQL wire proxy (Go + Calcite sidecar), multi-DB connectors (MySQL, SQL Server, Oracle, Snowflake, BigQuery, Databricks, MongoDB).
- Policy Administration Point (PAP): admin console (Next.js 14) + LangGraph AI graph for policy authoring + human-approval workflow.
- AI Orchestrator: LangGraph PEP graph for permission-aware NL→SQL with AST-first generation; RAG with per-chunk ACLs; LiteLLM routing by content classification.
- Audit pipeline: Kafka → ClickHouse (hot) + S3 WORM Object Lock (cold); tamper-evident hash-chain in PostgreSQL.
- UEBA: Apache Flink V1 statistical baseline; risk score as first-class ABAC variable; auto-response playbooks; break-glass dual-approval workflow.
- Infrastructure: Kubernetes (EKS/GKE/AKS), Linkerd service mesh, active-active reads, single-writer-with-failover multi-region, managed stateful services (Aurora, ElastiCache, MSK/Confluent, ClickHouse Cloud, S3).

Tech stack:
- Backend services: Go (primary), TypeScript (admin console + SDKs), Python (ML / notebooks), Java (Calcite sidecar).
- Auth: Ed25519-signed JWTs (identity only; roles resolved server-side); SessionContext schema (Zod → TS → Go codegen).
- Database: PostgreSQL with RLS ENABLE + FORCE on all tenant-scoped tables; SET LOCAL discipline on every query.
- Messaging: Apache Kafka (idempotent producer, acks=all, disk buffer fallback).
- Feature flags: Unleash — every new capability gated before merging to main.
- Migrations: forward-only via golang-migrate or Atlas — no down migrations in production.
- Observability: OpenTelemetry from day zero in every service (traces + metrics + logs); Prometheus + Grafana; Jaeger.
- Monorepo layout: apps/{service-name}/, packages/{shared-lib}/, infra/terraform/, infra/helm/, infra/docker-compose.yml, scripts/.

=== THIS PHASE ===

Phase number : [PHASE_NUMBER]
Phase name   : [PHASE_NAME]
Plan file    : [PHASE_FILE]
Owner        : [PHASE_OWNER]
Estimated    : [ESTIMATED_WEEKS] weeks
Hard deps    : [HARD_DEPS]
Feature flag : [FEATURE_FLAG_NAME]

Read the plan file [PHASE_FILE] in full before writing any code. The plan defines:
1. Phase objective and business purpose.
2. Scope boundaries (in scope / out of scope).
3. Detailed sub-phases and implementation tasks.
4. Architectural gaps and missing requirements you must address.
5. Edge cases and failure modes with mitigations.
6. Non-functional requirements (scalability, security, multi-tenant isolation, concurrency, performance).
7. Recommended improvements (architecture, DX, UX, reliability, observability, maintainability).
8. Technical considerations: DB design, API contracts, RBAC, validation flows, caching, queues, background jobs, audit logs, retry/idempotency, monitoring, CI/CD.
9. Risks, rollback strategy, future extensibility.
10. Deliverables and acceptance criteria.
11. Production readiness checklist.
12. Remaining risks carried forward.

=== IMPLEMENTATION INSTRUCTIONS ===

Work through sub-phases in the order defined in section 4 of the plan. For each sub-phase:

1. SCHEMA FIRST. Write and apply the database migration (forward-only). Enable RLS + FORCE on every new tenant-scoped table. Add the hash-chain trigger to any audit table. Use pg_partman for high-volume tables.

2. SERVICE SKELETON. Create the service directory under apps/ with:
   - main.go (or index.ts) with graceful shutdown (SIGTERM → drain → exit 0).
   - config struct loaded from environment variables (no hardcoded values).
   - OTel SDK initialized before any other dependency (traces + metrics exporter).
   - Health check endpoint: GET /healthz (liveness) and GET /readyz (readiness).
   - Feature flag check at startup — if the flag is off, the service logs and exits cleanly.

3. CORE LOGIC. Implement the business logic described in the sub-phase. Follow the plan's data models, algorithm descriptions, and API contracts exactly. Where the plan gives pseudo-code, translate it directly; do not simplify or skip steps.

4. API LAYER. Implement the REST / gRPC / WebSocket endpoints listed in section 9.2. Version all REST endpoints under /v1/. Return structured errors (code, message, request_id). Add OTel span for every handler.

5. RBAC. Wire the permission checks listed in section 9.3. Every privileged action must verify the caller's role against the PDP before proceeding.

6. CACHING. Implement the caching strategy from section 9.5. Use versioned cache keys. Add pub/sub invalidation. Use single-flight (singleflight.Group in Go) to prevent stampede.

7. QUEUES & BACKGROUND JOBS. Implement the jobs from section 9.6. All jobs must be idempotent. Use idempotency keys on every Kafka produce. Jobs must emit OTel spans and metrics.

8. AUDIT LOGGING. Every mutation and every privileged read must emit an audit event via the packages/audit-client SDK. Fields: tenant_id, actor_id, action, resource_type, resource_id, outcome, metadata, trace_id.

9. RETRY & IDEMPOTENCY. Apply the patterns from section 9.8. Outbound HTTP calls: exponential backoff with jitter, max 5 retries. Kafka consumers: at-least-once with idempotent processing on event_id.

10. TESTS. Write:
    - Unit tests for all pure functions and domain logic.
    - Integration tests against real PostgreSQL and Redis (no mocks for storage).
    - Contract / conformance tests for every external API surface.
    - Property-based tests (rapid / gopter) for any parser, DSL, or algorithm with invariants.
    - The test for every edge case listed in section 6 of the plan.

11. OBSERVABILITY. For every new code path add:
    - An OTel span with relevant attributes (tenant_id, user_id, resource, outcome).
    - A Prometheus counter or histogram (latency_seconds, errors_total, cache_hit_ratio).
    - A structured log line (zerolog / zap) at key decision points (not chatty).
    - An alert rule (Prometheus PrometheusRule YAML) for the SLOs in section 9.9.

12. FEATURE FLAG GATE. Wrap the entire feature in the Unleash flag [FEATURE_FLAG_NAME]. The flag must be checked:
    - At service startup (skip registration if off).
    - At the HTTP / gRPC handler level (return 404 feature_disabled if off).
    - In the PDP policy evaluation path if this phase adds a new ABAC variable.

13. CI. Add or update:
    - A GitHub Actions workflow that runs the new service's tests in isolation.
    - A Docker build step that pushes the image to the registry.
    - A Helm chart in infra/helm/{service-name}/ with HPA, PDB, NetworkPolicy, ServiceAccount + IRSA annotation, OTel sidecar config, and resource limits.
    - An ArgoCD Application manifest in infra/argocd/.

=== QUALITY GATES (non-negotiable) ===

- No hardcoded tenant_id, user_id, or secrets anywhere in the codebase.
- SET LOCAL ROLE + set_config('app.*', ..., true) called before every PostgreSQL query in session context. Never call SET ROLE without LOCAL.
- RLS FORCE enabled on every tenant-scoped table — verified by integration test that checks cross-tenant isolation.
- Every Kafka producer: enable.idempotence=true, acks=all, compression=zstd.
- Every outbound webhook: HMAC-SHA256 signed; SSRF defense (DNS resolution + private-range denylist) applied before connecting.
- Maximum policy DSL depth ≤ 5, maximum nodes ≤ 256, regex engine RE2 only — enforced by validator with fuzz tests.
- No raw SQL string concatenation with user input anywhere — parameterized queries or AST construction only.
- Every breaking API change introduced under a new version namespace (/v2/); old version kept with deprecation header for ≥ 6 months.
- Migrations are forward-only. If you need to undo, write a new migration. Never edit an already-applied migration file.
- All secrets accessed via Vault (or external-secrets-operator in K8s). No secrets in environment variable files committed to git.

=== OUTPUT FORMAT ===

For each sub-phase, produce output in this order:
1. **Migration file** (if schema changes): `migrations/NNNN_description.up.sql`
2. **Implementation files**: full file contents, organized by directory.
3. **Test files**: alongside implementation in `_test.go` or `*.test.ts`.
4. **Helm chart delta** (if a new service): `infra/helm/{service}/`.
5. **Alert rules** (if new SLOs): `infra/monitoring/{service}-alerts.yaml`.
6. **Feature flag registration** (if new flag): update `infra/unleash/flags.yaml`.
7. **PROGRESS.md update**: change the phase row's Impl column to ✅ and add today's date + a one-line note to the Notes column.

After completing all sub-phases, summarize:
- What was built.
- Which acceptance criteria from section 11 are now satisfied.
- Which items from the production readiness checklist (section 12) are done.
- Any deviations from the plan and the reason.
- Remaining risks carried forward (from section 13).

=== START ===

Begin with Phase [PHASE_NUMBER] sub-phase [PHASE_NUMBER].1. Read [PHASE_FILE] now, then proceed.
```

---

## Quick-Fill Example (Phase 3 — PDP V1)

```
[PHASE_NUMBER]       → 3
[PHASE_NAME]         → PDP V1
[PHASE_FILE]         → docs/phases/03-pdp-v1.md
[PHASE_OWNER]        → Backend Lead
[ESTIMATED_WEEKS]    → 3
[HARD_DEPS]          → Phases 0, 1, 2 complete and merged to main
[PRIMARY_LANGUAGE]   → Go
[FEATURE_FLAG_NAME]  → pdp-v1
```

---

## Session Continuation Prompt

If an implementation session is interrupted and you need to resume, use this shorter prompt at the start of the next session:

```
We are implementing Phase [PHASE_NUMBER] — [PHASE_NAME] — of the AI-Native Authorization & Retrieval Governance Platform.

The plan is at [PHASE_FILE]. We completed sub-phases [COMPLETED_SUBPHASES] last session. 

Resume from sub-phase [NEXT_SUBPHASE]. Apply all quality gates from the original implementation prompt (SET LOCAL discipline, forward-only migrations, feature flag [FEATURE_FLAG_NAME], OTel from day zero, idempotency keys, audit-everything, RLS FORCE, no raw SQL concat, HMAC signing on webhooks).

Continue where we left off.
```

---

## Phase-Specific Overrides

Some phases require extra instructions beyond the base template. Add them as a section below the `=== START ===` line:

### Phase 0 (Foundation)
> Add: "Do not implement any business logic. Scaffold only: monorepo layout, Docker Compose, CI workflows, OTel SDK, Vault dev mode, Unleash, seed scripts. Every service template must be copy-pasteable for future phases."

### Phase 3 (PDP V1)
> Add: "The Conditions DSL parser must be fuzz-tested. Include gopter property tests proving: monotonicity (more policies → same or fewer allowed columns), determinism (same inputs → same decision), tenant containment (no policy from tenant A affects tenant B). These are acceptance-blocking."

### Phase 6 (PEP PostgreSQL Proxy)
> Add: "The proxy must never leak original table/column names in error messages. Every error response to the client must use the generic message 'permission denied' regardless of the actual DB error. Write a test that verifies error text scrubbing."

### Phase 9 (AI PAP Graph)
> Add: "Every LLM call must go through the constrained decoding path (Anthropic tool_use or OpenAI json_schema). Raw free-text LLM output must never be parsed as a policy. The human-approval step is mandatory and cannot be bypassed by a feature flag."

### Phase 13 (Anomaly Detection)
> Add: "The Flink job must be deterministic: given the same audit event stream twice, it must produce the same score series. Write a replay test that verifies this. Tag every score with model_version; comparisons across versions are forbidden without explicit opt-in."

### Phase 14 (Auto-Response)
> Add: "The tenant pause-auto-response flag must halt all auto-response decisions within 5 seconds. Write an integration test for this SLO. Break-glass approval must be atomic — use a DB transaction with SELECT FOR UPDATE on the session row to prevent double-approval."

### Phase 15 (Scale-Out)
> Add: "The DR drill is mandatory before this phase can be marked complete. Script the drill as a runbook and execute it in staging. Record RTO and RPO achieved. Both must meet plan targets (RTO < 30 min, RPO < 5 min) before GA gating."

### Phase 16 (Compliance & GA)
> Add: "Do not ship any new features in this phase. The scope is hardening, evidence collection, documentation, SDK polish, and launch operations only. Any feature work discovered must be ticketed for post-GA."
