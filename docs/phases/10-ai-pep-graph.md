# Phase 10 — AI Orchestrator: PEP Graph (Natural Language → Safe SQL)

> **Duration:** 20–23 weeks (≈3 weeks focused) &nbsp; · &nbsp; **Owner:** AI Engineer + Backend &nbsp; · &nbsp; **Dependencies:** Phases 0–9
> **Companion:** [`../implementation-plan.md` §Phase 10](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

End-user asks "show me overdue invoices this quarter" in chat → the **PEP graph** of agents resolves permissions, generates a SQL AST constrained to the user's Allowed Schema Snapshot, validates the AST against a strict allowlist, runs a cost gate, executes via the PEP proxy with full `SessionContext`, streams results back, and applies any output-side masks.

**Business rationale:** "chat with your data" without governance is the leading source of accidental data exposure. With governance, it's a productivity multiplier — finance analysts answer their own questions instead of filing tickets, and every byte returned is provably within policy. This phase completes the AI-native promise.

---

## 2. Scope Boundaries & Ownership

**In scope**
- PEP graph in `apps/ai-orchestrator`: Input Sanitizer → Permission Resolver → Retriever → SQL Drafter (AST) → AST Validator → Cost Estimator → Proxy Executor → Result Formatter.
- AST-first generation (Calcite-compatible JSON), with raw-SQL fallback.
- Strict validator: tables/columns allowlist, function allowlist, JOIN-via-FK only, single-statement.
- Cost gate via `EXPLAIN`.
- Result streaming row-by-row with output-side mask application.
- "Chat with your data" UX in admin console (or a separate user app).
- Saved queries + thumbs feedback.

**Out of scope**
- Multi-DB engines beyond PG (Phase 11).
- Risk-aware mid-flight termination (Phase 14).
- Auto-generated dashboards / charts (deferred).
- Multi-modal output (charts, files) beyond tables.

**Ownership**
- **Drives:** AI Engineer.
- **Reviews:** Security (validator, side channels), Backend (proxy integration), Product (chat UX).

---

## 3. Hard Dependencies & Sequencing

- Phase 6 PG proxy + Calcite parse/validate.
- Phase 8 Allowed Schema Snapshot.
- Phase 3 PDP for permission resolution.
- Phase 9 orchestrator infrastructure (token budgets, telemetry).

Sequencing: graph skeleton → permission resolver → retriever → drafter (constrained AST) → validator → cost gate → proxy executor → streaming → chat UX → eval set → red-team.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 10.1 — PEP Graph Nodes

```
[Input Sanitizer]      → strip control sequences; bound length
[Permission Resolver]  → PDP BulkDecide; build Allowed Schema Snapshot
[Retriever]            → vector search over filtered schema_metadata
                          for top-K relevant tables/columns
[SQL Drafter]          → frontier model; constrained decoding;
                          output = AST (Calcite-compatible JSON) preferred,
                          raw SELECT-only SQL acceptable fallback
[AST Validator]        → tables/columns allowlist; function allowlist;
                          single SELECT; no DDL/DML; FK-only JOINs;
                          LIMIT required and ≤ role cap
[Cost Estimator]       → run EXPLAIN via proxy; reject if too expensive
[Proxy Executor]       → submit to Phase 6 proxy with SessionContext
[Result Formatter]     → stream rows to client; apply output-side masks;
                          attach citations & policy attributions
```

### 10.2 — Constraint: Emit AST, Not Strings

Drafter outputs a structured Calcite-compatible AST in JSON:
```json
{
  "kind": "Select",
  "distinct": false,
  "select": [{ "kind": "Column", "table": "invoices", "column": "id" }, …],
  "from": { "kind": "Table", "name": "public.invoices" },
  "where": { "kind": "And", "args": [
    { "kind": "Cmp", "op": ">=", "lhs": {"kind":"Column","table":"invoices","column":"due_date"}, "rhs": {"kind":"Literal","value":"2026-04-01"} },
    { "kind": "Cmp", "op": "=",  "lhs": {"kind":"Column","table":"invoices","column":"status"}, "rhs": {"kind":"Literal","value":"OVERDUE"} }
  ]},
  "order_by": [{ "column": "due_date", "dir": "desc" }],
  "limit": 1000
}
```

Why AST:
- Grammar-constrained → much harder to inject.
- Validator works on AST directly (cheap, fast).
- Renders to any dialect via Calcite `SqlDialect`.
- Easier to reason about deterministically.

Fallback: raw SQL → immediate Calcite parse → AST. Used when provider doesn't support deeply nested JSON schemas.

### 10.3 — Validator Rules

Reject if ANY of:
- Statement type ≠ `SELECT`.
- Any referenced table not in Allowed Schema Snapshot.
- Any referenced column not in snapshot for its table.
- Any function not in the per-tenant allowlist (default: `sum, count, avg, min, max, coalesce, date_trunc, lower, upper, extract, length, abs, round`).
- Subqueries (V1 deny; enable in V1.5 with depth ≤ 1).
- Multiple statements (semicolons).
- Comments in body (strip + re-parse).
- Cartesian products (`FROM a, b` without `WHERE` predicate).
- Missing `LIMIT` or `LIMIT > role.maxRows`.
- JOINs not declared in the snapshot's FK graph.
- Window functions (V1 deny; enable later).
- CTEs (V1 deny; enable in V1.5).
- Recursive constructs (always deny).

Validator emits structured rejection reasons; UI surfaces them with hints ("`amounts` is not visible to your role; try `total` instead").

### 10.4 — Cost Gate

Reuses Phase 6 cost gate:
1. Submit rewritten SQL via proxy with `EXPLAIN` action.
2. Parse `Total Cost` and `Plan Rows`.
3. Reject if exceeding role-derived caps.
4. Surface "try narrowing the date range" guidance.

Cost gate is *advisory* for tiny queries; *blocking* for queries with `Total Cost > role.maxCost`.

### 10.5 — Proxy Executor

- Submits the validated SQL to the Phase 6 proxy with user's SessionContext.
- The proxy applies row filters / column masks per PDP decision; the AI never bypasses.
- Streams `RowDescription` + `DataRow` via gRPC streaming back to the orchestrator.

### 10.6 — Result Formatter & Streaming

- Long result sets stream row-by-row.
- Output-side mask functions applied (e.g., `mask_email_domain`) — typically already done by proxy; safety re-apply.
- Citation block: which policy permitted access, which tables sourced; rendered as expandable.
- Progressive rendering on UI: incremental table rows; user can scroll while still streaming.
- 10k-row safety cap on UI rendering; larger queries get "download CSV" CTA (cap by plan tier).

### 10.7 — Chat With Your Data UX

A view (in admin console or stand-alone user app):
- **Conversation** thread with NL input.
- **Resolved schema** collapsible panel (shows the snapshot used).
- **Generated SQL** expandable (with "edit and re-run" power-user mode).
- **Result table** progressive rendering.
- **Save as named question** for repeat use.
- **Thumbs up/down feedback**; comments captured for evals + future fine-tuning.
- **Privacy badge**: shows which LLM provider answered + classification routing.

### 10.8 — Saved Questions

- A user saves a question; it persists `(promptHash, snapshotHash, sql)`; re-run uses cached SQL if snapshot/policies unchanged.
- Versioned; admin can publish team-wide questions.
- Webhook trigger reserved (Phase 12) so saved questions can run on schedule.

### 10.9 — Eval Set + Red-Team

- 50–100 NL → expected-SQL pairs per tenant archetype.
- Adversarial corpus: "exfiltrate" prompts, SQLi-via-NL prompts, side-channel probes.
- CI gate on regression.

### 10.10 — Per-Tenant Configuration

- Function allowlist per tenant (super-admin can extend).
- LIMIT caps per role.
- Cost caps per role.
- Provider routing per classification (inherits Phase 8 config).
- "Allow subqueries / CTEs / windows" feature flags per tenant tier.

---

## 5. Architectural Gaps & Missing Requirements

1. **Aggregation row-count vs raw row-count.** Cost gate measures plan rows; an aggregation can read 10M rows to return 1. Distinguish via `Plan Rows` *and* `Total Cost`.
2. **Approximate vs exact answers.** Some tenants accept approx aggregates for cheaper queries; reserve a `query_mode ∈ {exact, approx}` field.
3. **Chart / visualization.** Tables only in V1; reserve a typed chart-spec output for V2.
4. **Time-zone semantics.** "this quarter" depends on user's TZ. Carry `subject.timezone`; normalize in the AST.
5. **Multi-table joins beyond FK graph.** Tenants will request joins via non-FK columns (composite keys). Define `inferred_join_keys` schema.
6. **Natural-language follow-ups.** "Now break it down by region" — multi-turn context. V1 stateless; design conversation memory.
7. **Output-side residency.** A result set containing `restricted` content must not be streamed to a non-private client (e.g., browser); design boundaries.
8. **Saved-question scheduling.** Reserved for Phase 12 webhooks/schedules; design now.
9. **Result caching.** Identical query + same policy_set_version → reuse result for 30 s. Document cache-coherency.
10. **Streaming-cancellation back to proxy.** If user closes the tab, proxy must cancel backend query promptly.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Drafter hallucinates a column                                     | Validator rejects deterministically; UI suggests valid alternatives.                              |
| AST emits a subquery (V1 disallowed)                              | Validator rejects with friendly explanation; offers a refactor hint.                              |
| Cost gate rejects query                                           | UI proposes narrowing (date range, region filter, limit).                                         |
| Result set huge (10M+ rows)                                       | Proxy caps; AI streams first 10k; user offered download.                                          |
| User closes tab mid-query                                         | Orchestrator detects disconnect; proxy issues `CancelRequest` to backend.                         |
| Snapshot stale during execution                                   | Refresh snapshot once, retry; abort with clear error if still stale.                              |
| Drafter cycles between rejections                                 | Max 2 retries; final rejection surfaces context to user with "ask differently?" suggestion.       |
| User asks about data they cannot see                              | Snapshot empty → AI answers "no accessible data for this question" gracefully.                    |
| Provider outage                                                   | Failover to secondary; cost may change; surface to user.                                          |
| Validator regression                                              | Eval set + golden set in CI; alarms.                                                              |
| Adversarial prompt with embedded SQL                              | Sanitizer + validator; raw SQL from input ignored entirely (drafter ignores anything that looks like SQL injection). |
| Multi-tenant isolation breach                                     | Snapshot is the only schema source; validator rejects anything outside snapshot.                  |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Stateless orchestrator; HPA on concurrent runs.
- Snapshot cache hits make Permission Resolver near-free.
- Drafter is the dominant latency; choose model carefully per tenant tier.

### 7.2 Security
- Validator is *the* trust boundary; treated as security-critical code.
- Generic errors to end users; specifics to audit only.
- Side-channel bounds: uniform timing for rejection paths.
- Provider routing per classification (Phase 8).
- Result-set classification re-checked at output edge.

### 7.3 Multi-Tenant Isolation
- Snapshot strictly tenant-scoped.
- Drafter's system prompt includes tenant-id context; never reused across tenants in the same process worker.

### 7.4 Concurrency
- Per-conversation queue; one in-flight query per conversation.
- Per-tenant token bucket.

### 7.5 Performance
- p95 end-to-end < 5 s (NL → first row).
- p95 first-row latency < 3 s.
- p99 full-result streaming for 100k rows < 30 s.

---

## 8. Recommended Improvements

### Architecture
- A shared `SqlValidator` library (Go + TS) — used by orchestrator AND Phase 6 proxy for any tenant-supplied SQL — single source of correctness.
- `RetrievalService` extraction (Phase 8) makes the graph easier to test.
- Speculative parallel runs: Permission Resolver + Retriever concurrently.

### DX
- Per-tenant prompt configuration tested in CI.
- Replay tooling for production conversations (read-only).
- A `pep-cli generate --prompt='…' --user --data-source` for offline debugging.

### UX
- "Why this SQL?" panel showing the Drafter's reasoning steps (sanitized).
- "Why was this denied?" with policy attribution (links to Phase 4 Explain).
- Saved questions catalog with categories.
- Keyboard shortcuts for power users.

### Reliability
- Provider failover + per-provider health.
- Snapshot refresh-and-retry.
- Cancellation propagation end-to-end.

### Observability
- Per-graph-node spans + token cost.
- Validator rejection histogram (top rejection reasons → product backlog).
- End-to-end latency dashboards.
- Per-tenant "AI usage" panel.

### Maintainability
- ADRs: AST vs raw SQL, validator boundaries, provider abstraction, cost-gate thresholds.
- Eval set in repo with regression tests.

---

## 9. Technical Considerations

### 9.1 DB Design
- `ai_pep_sessions(id, tenant_id, user_id, prompt, ast_json, sql, decision_id, cost_usd, rows_returned, started_at, ended_at, status)`.
- `saved_questions(id, tenant_id, user_id, name, prompt, sql, snapshot_hash, last_run_at)`.
- `pep_evals(...)` for the eval corpus.

### 9.2 API Contracts
- `POST /v1/ai/pep/ask` (SSE).
- `GET /v1/ai/pep/sessions/{id}`.
- `POST /v1/ai/pep/saved-questions`.
- `POST /v1/ai/pep/saved-questions/{id}/run`.

### 9.3 RBAC
- `ai.ask.data` permission required.
- `ai.saved.publish` for team-wide questions.

### 9.4 Validation Flows
Covered above. Validator is the trust boundary; multi-layered with Phase 6 proxy.

### 9.5 Caching
- Snapshot cache (Phase 8).
- Result cache (30 s, keyed by `(userId, querySha, policySetVersion)`).
- Drafter token cache (identical prompt + snapshot).

### 9.6 Queues & Background Jobs
- Saved-question scheduler (Phase 12 wires).
- Eval-suite runner nightly.
- Cost rollup hourly.

### 9.7 Audit Logs
Each ask emits: `{user, prompt, ast, sql, decision, rows, cost, provider, model}` — sensitive prompts hashed if needed.

### 9.8 Retry & Idempotency
- `Idempotency-Key` on `/ask`.
- No automatic LLM retry; user can re-ask.

### 9.9 Monitoring
Alerts: validator rejection rate spike, drafter latency > 6 s p95, cost > 200% baseline, user thumbs-down rate.

### 9.10 CI/CD
- Eval set blocks release on regression.
- Provider mocks for unit, real for `staging`.
- Adversarial corpus run on every release.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                  | Likelihood | Impact   | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Hallucinated JOINs                                                    | High       | High     | FK-only JOINs enforced; validator rejects.                                                       |
| Drafter rejection rate > 10%                                          | Med        | High     | Drafter has drifted; investigate before adding more LLM rules.                                  |
| Cost-gate too strict → users frustrated                               | Med        | Med      | Per-role caps tunable in admin UI; alarm on rejection rate.                                      |
| Side-channel leakage via error variation                              | Med        | High     | Uniform error class + structured reason only in audit.                                           |
| Drafter exfiltration prompt                                           | Med        | High     | Sanitizer + Validator + Provider safety + Audit + Anomaly (Phase 13).                            |
| Mass concurrent users overwhelm pool                                  | Med        | High     | Per-tenant rate-limit; queue.                                                                    |
| Saved query persists stale SQL after schema rename                    | High       | Med      | Validate on every run; auto-suggest rewrite.                                                     |

### Rollback
- Feature flag per tenant.
- Per-graph-node version pinning.
- Provider failover.

### Future Extensibility
- Multi-turn conversations.
- Chart generation.
- Streaming-cancellation enriched with risk feedback.
- Tenant-specific fine-tuning (Phase 13+).
- DM-level integration (Slack: "ask Janus, what's our revenue?").

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] PEP graph deployed.
- [ ] AST-first generation with raw-SQL fallback.
- [ ] Strict validator with structured rejection reasons.
- [ ] Cost gate via proxy `EXPLAIN`.
- [ ] Streaming proxy executor.
- [ ] Chat UX with saved questions.
- [ ] Eval set + adversarial corpus in CI.

### Acceptance Criteria
- [ ] End-to-end NL → safe SQL → results in < 5 s p95.
- [ ] Validator rejects > 99% of off-allowlist generated SQL.
- [ ] AST-based generation produces dialect-correct SQL via Calcite.
- [ ] Cost gate prevents queries scanning > 1M rows by default.
- [ ] Result streaming for 100k rows without OOM.
- [ ] Adversarial exfiltration prompts blocked.

---

## 12. Production Readiness Checklist

- [ ] Provider failover tested.
- [ ] Cost dashboards + alerts.
- [ ] Cancellation propagation tested.
- [ ] DR runbook: drafter outage, provider outage, validator regression.
- [ ] Per-tenant configuration documented + tested.
- [ ] Red-team report + remediation.

---

## 13. Remaining Risks Carried Forward

- **Only PG** until Phase 11.
- **No mid-flight risk-based cancellation** until Phase 14.
- **No multi-turn conversation** in V1.
- **No charts** in V1.
- **Subqueries / CTEs / windows** behind feature flags.
- **Provider drift** managed by version pins + CI guards.
