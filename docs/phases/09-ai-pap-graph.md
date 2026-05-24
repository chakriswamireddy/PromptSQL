# Phase 9 — AI Orchestrator: PAP Graph (Policy Authoring by Natural Language)

> **Duration:** 17–20 weeks (≈3 weeks focused) &nbsp; · &nbsp; **Owner:** AI engineer + Backend &nbsp; · &nbsp; **Dependencies:** Phases 0–8
> **Companion:** [`../implementation-plan.md` §Phase 9](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Admin authors a policy in English — "Give finance managers read-only access to payments for their own campus, hide bank account numbers" — and the **Policy Administration Point (PAP) graph** of agents drafts a strictly-typed JSON policy, validates it deterministically, runs it through the simulator, generates a human-readable explanation, and presents it for **mandatory human approval**. No auto-apply, ever.

**Business rationale:** policy authoring is the highest-friction surface for non-engineers. Natural-language drafting + simulator preview + human approval converts "I need to wait for ops to grant me access" into a same-day, transparent, auditable workflow. The simulator + Adversarial Reviewer pair makes the AI's confident wrongness *visible*, not silent.

---

## 2. Scope Boundaries & Ownership

**In scope**
- `apps/ai-orchestrator` Node.js + Fastify + LangGraph service.
- PAP graph nodes: Input Sanitizer → Intent Parser → Schema Resolver → Policy Drafter → Policy Validator → Simulator → Audit Explainer → Human Approval → Compiler.
- Constrained decoding (tool-use / JSON schema) for drafter.
- Idempotency keys, token budgets, cost telemetry.
- Optional Adversarial Reviewer (V2 enhancement; in-phase if budget allows).
- Admin console wiring: NL prompt → streaming output → JSON in Monaco → simulator → approval.

**Out of scope**
- PEP graph (NL → SQL) (Phase 10).
- Auto-apply (never).
- Risk-aware drafting (Phase 13+).
- Voice / multi-turn agentic drafting (deferred).

**Ownership**
- **Drives:** AI Engineer.
- **Reviews:** Security (LLM risk, injection, output validation), Backend (compiler integration), Product (UX).

---

## 3. Hard Dependencies & Sequencing

- Phase 3 PDP `Validate` + `Explain`.
- Phase 4 Admin console simulator + Monaco editor.
- Phase 7 schema metadata.
- Phase 8 retrieval primitives.

Sequencing: graph skeleton → sanitizer → intent parser → schema resolver → drafter (constrained) → validator → simulator integration → explainer → human approval surface → compiler → tests → cost dashboards → optional adversarial reviewer.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 9.1 — Service Shape

`apps/ai-orchestrator` (Node.js + TS + Fastify + LangGraph + Zod):
- Streams Server-Sent Events (SSE) to the admin console.
- gRPC for service-to-service.
- OTel + per-span LLM attributes (provider, model, tokens, cost, latency).
- Per-tenant token budgets enforced by middleware.

### 9.2 — PAP Graph Nodes

```
[Input Sanitizer]      → strip control sequences; bound length; rate-limit
[Intent Parser]        → classify: role.create | policy.update | grant | revoke
                          (small model: Haiku, GPT-4o-mini; structured output)
[Schema Resolver]      → map fuzzy terms to canonical names via RAG over schema_metadata
                          (filtered by admin's permissions)
[Policy Drafter]       → frontier model (Sonnet, GPT-4o)
                          (constrained decoding; output = Policy JSON)
[Policy Validator]     → deterministic checks: tables exist, no privilege escalation,
                          no cross-tenant, no conflict with active deny rules
[Simulator]            → runs draft across synthetic + real personas; produces diff
[Audit Explainer]      → small model produces plain-English explanation + delta
[Human Approval]       → ALWAYS; SSE-driven UI state; idempotency-key required
[Compiler]             → writes draft to policies table; outbox event for invalidation
```

Each node:
- Has typed Zod I/O schemas.
- Returns structured errors (no auto-repair from invalid LLM output).
- Records OTel span + token usage.
- Has bounded retry (max 1 retry, with backoff).

### 9.3 — Graph Bounds & Safeguards

- **Max iterations:** 6 nodes per request (no cycles).
- **Wall-clock budget:** 30 s; abort cleanly with structured error.
- **Per-tenant token budget:** rolling minute + day; throttle at 80%, hard-stop at 100%.
- **Cost budget:** per-tenant daily USD cap configurable.
- **Idempotency:** compiler step keyed by `(authorId, draftHash, prompt)`; replays return existing draft.
- **No silent retry of LLM calls.** A failed Drafter run surfaces to user with a "report to support" button.

### 9.4 — Constrained Decoding

For Drafter:
- Anthropic: `tool_use` mode with a single tool whose input schema = `Policy` Zod-derived JSON Schema.
- OpenAI: `response_format = { type: "json_schema", strict: true, schema: … }`.
- Bedrock-hosted Anthropic: same as direct (`anthropic.beta.tools.use`).

Zod backstop validates at node boundary. Malformed → node fails (no auto-retry).

### 9.5 — Show The JSON (Mandatory UX)

Admin console output:
1. Original NL prompt.
2. **Generated JSON policy** in Monaco (editable).
3. English explanation from Audit Explainer.
4. **Simulator preview** alongside.
5. Cost + tokens used for transparency.
6. Approve / Reject buttons; approval routes through Phase 4 workflow.

**Critical rule:** the admin approves *the JSON*, not the English. The English helps comprehension; the JSON is what enforces. This must be a UX commitment with no shortcuts.

### 9.6 — Adversarial Reviewer (Optional in V1; recommended V2)

After Drafter:
> "You are a red team. Find ways this policy could leak data, be misused, or allow privilege escalation. List specific attack scenarios."

For each returned scenario, the Simulator executes a probe. Successful attacks block approval and feed back as constraints for refinement. Reviewer LLM uses a *different* provider when feasible to reduce shared-model blind spots.

### 9.7 — Schema Resolver

Schema Resolver is the system's defense against column hallucination:
1. Query `schema_metadata` via `Allowed Snapshot` (Phase 8) — filtered to admin's permissions.
2. RAG over column descriptions to map fuzzy terms ("payments", "campus") to canonical names.
3. Return a map `fuzzy → canonical` with confidence scores.
4. Drafter is *required* to use only canonical names from the resolver map.

If a fuzzy term has no high-confidence match, the resolver surfaces an interactive disambiguation in the UI.

### 9.8 — Policy Validator (Deterministic)

Beyond Zod schema:
- Every referenced column exists in `schema_metadata`.
- Every referenced role exists in `roles` (tenant-scoped).
- No privilege escalation: a drafter who is not super-admin cannot author a policy that grants super-admin permission.
- No cross-tenant references.
- No conflict with active deny rules (deny would silently override the new allow — warn).
- DSL boundedness re-verified (depth/nodes).

### 9.9 — Audit Explainer

Small model converts the JSON policy + simulator diff into a 3-paragraph English description:
- *Who it affects.*
- *What changes (added / removed / restricted).*
- *Risks and obligations.*

Cached per `(policyHash, simulatorDiffHash)`; deterministic by seed.

### 9.10 — Human Approval Surface (Phase 4 wiring)

- Approval flow lives in Phase 4. Phase 9 ensures the Compiler step *blocks* on approval state.
- SSE streams approval-pending state to admin console.
- Approval requires fresh MFA (≤ 5 min, per Phase 4 policy).

### 9.11 — Compiler

- Writes draft to `policies` (`status='draft'`).
- Stamps `created_by_ai = true`, `model_metadata = {provider, model, prompt_hash, cost}`.
- Outbox event for invalidation.
- Submit-for-review action lives in Phase 4 endpoints.

### 9.12 — Tests

- **Unit:** each node with mocked LLM responses; schema-conform outputs.
- **Integration:** end-to-end NL → draft → simulator → approval → activation.
- **Adversarial:** seed prompts with injection attempts; verify graph stops at sanitizer.
- **Eval set:** 50 NL prompts with expected canonical Policy JSON; CI gate (≥ 90% pass).
- **Cost test:** average session < $0.10; alarm if regressed > 50%.

---

## 5. Architectural Gaps & Missing Requirements

1. **Prompt-version control.** Prompts evolve; treat them as code. `apps/ai-orchestrator/prompts/*.md` versioned, with CI snapshot tests on key prompts.
2. **Model-version pinning.** Anthropic / OpenAI silently update models. Pin exact model IDs + change-management process.
3. **Provider failover.** Drafter on Sonnet falls back to GPT-4o if Anthropic outage; behavior must be tested.
4. **Multi-turn drafting.** A user might say "make that more restrictive." Phase 9 is single-turn; reserve a `conversationId` and design for it.
5. **Policy-template hinting.** A "show me similar past policies" retrieval helps the drafter; reserve `policies` semantic search.
6. **Confidentiality of admin prompts.** Even admin prompts could contain sensitive info; route per classification (no `restricted` content to external).
7. **Citation rendering.** When the resolver maps "payments" → `public.payments`, surface that in the UI as a citation.
8. **Internal vs. external admin distinction.** Some admins author for their own tenant; some for many. The graph must enforce tenant containment regardless.
9. **Deterministic seeds.** Document expected determinism: Drafter has temperature 0.2 → not deterministic. Explainer can be temperature 0 + seed.
10. **Token-cost forecast preview.** Estimate cost before running the graph (rough), show in UI.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Drafter hallucinates a column                                     | Validator rejects; explainer surfaces missing column; UI proposes correction.                    |
| Provider outage                                                   | Failover to secondary; surface delayed start to user.                                            |
| Token budget exhausted mid-graph                                  | Graceful abort with "resume tomorrow"; partial work saved as draft if Drafter succeeded.         |
| LLM emits JSON that *almost* matches schema                       | No auto-repair; UI shows error with structured diff and a "try again" button.                    |
| Admin disagrees with Drafter; edits JSON                          | Edited JSON re-runs Validator + Simulator without re-calling Drafter (cost-saving).              |
| Simulator timeout                                                 | Phase 4 / Phase 9 share a timeout budget; surface partial results with warning.                   |
| Cross-tenant resource referenced (admin error)                    | Validator rejects with explicit reason.                                                          |
| Adversarial Reviewer finds attack                                 | UI blocks approval; offers refinement loop.                                                      |
| Per-tenant cost cap reached                                       | UI offers "upgrade plan" CTA or "ask billing admin."                                              |
| Prompt-injection in NL input                                      | Sanitizer detects control phrases; refuse + audit.                                               |
| Idempotency-key collision                                         | Second request returns existing draft, not a new one.                                            |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Orchestrator is stateless; HPA on concurrent graph runs.
- Per-tenant queue prevents one tenant from starving others.

### 7.2 Security
- Sanitizer + delimiter wrapping + provider safety APIs (three layers).
- LLM provider keys per-environment, rotated quarterly.
- Per-tenant DPA tier respected by routing.
- Outputs validated deterministically before any side effect.

### 7.3 Multi-Tenant Isolation
- Per-tenant token bucket, cost cap, draft store.
- Resolver runs against tenant-scoped schema only.
- Drafts saved per-tenant; never visible cross-tenant.

### 7.4 Concurrency
- LangGraph state per-request, no shared mutable state.
- Compiler write is atomic + idempotent.
- Approval is single-writer per draft.

### 7.5 Performance
- p95 end-to-end < 10 s (excluding human approval).
- Median drafter call < 3 s.

---

## 8. Recommended Improvements

### Architecture
- Treat the **graph** as a versioned artifact (`pap-graph-v1.json` schema) so behavior changes are auditable.
- Distinct *Drafter* models per policy class (e.g., role.create vs row-filter) to optimize cost.
- A `policy_explainer` library shared by simulator + AI explainer for consistency.

### DX
- A `pap-cli draft --prompt='…'` runs the graph offline against fixtures.
- Replay tooling for production runs (read-only) to debug a tenant's experience.
- Per-prompt golden snapshots in CI.

### UX
- Live-streaming the graph as a vertical stepper: each node shows status, latency, cost.
- "Show me what changed" diff between current active and proposed draft.
- "Adversarial findings" panel listing scenarios the Reviewer caught.

### Reliability
- Health probe distinguishes provider health from orchestrator health.
- Circuit breakers per provider.
- Graceful degradation: if Drafter unavailable, allow manual JSON authoring (Phase 4 fallback).

### Observability
- OTel attributes per node: `pap.node`, `llm.provider`, `llm.model`, `llm.tokens.{in,out}`, `llm.cost_usd`, `pap.tenant_id`.
- Cost dashboards per tenant, model, node.
- Quality dashboards: eval-set pass rate per release.

### Maintainability
- ADR: LangGraph vs custom orchestration; provider abstraction; pinning policy.
- Prompts in `prompts/` with versioning, change diffs reviewed like code.

---

## 9. Technical Considerations

### 9.1 DB Design
- `ai_sessions(id, tenant_id, user_id, prompt, graph_run_jsonb, status, cost_usd, started_at, ended_at)`.
- `policies.created_by_ai bool`, `policies.ai_session_id`.
- `ai_evals(id, prompt_hash, expected, last_actual, last_run_at)` for the eval set.

### 9.2 API Contracts
- `POST /v1/ai/pap/draft` (idempotent, SSE).
- `POST /v1/ai/pap/refine` (multi-turn future).
- `POST /v1/ai/pap/explain` (re-explain existing policy).

### 9.3 RBAC
- `ai.draft.policy` required.
- Drafter cannot author policies the admin themselves can't author (tenant- + role-scoped).

### 9.4 Validation Flows
- Zod at every boundary.
- Validator + Adversarial Reviewer.
- Simulator runs before approval.

### 9.5 Caching
- Identical prompt + schema-version + policy-set-version → cached draft for 60 min.
- Explainer cached on hashes.
- Resolver cached per (tenantId, fuzzy term).

### 9.6 Queues & Background Jobs
- Drafter queue per-tenant for fairness.
- Eval-set runner nightly in CI.
- Cost rollup hourly.

### 9.7 Audit Logs
- Every node emits a span + audit event.
- Approval / rejection audited.
- Cost telemetry to billing pipeline (Phase 16 wires).

### 9.8 Retry & Idempotency
- Idempotency-key on `/draft`.
- No automatic LLM retry; single failure surfaces to user.
- Compiler step idempotent.

### 9.9 Monitoring
Alerts: graph error rate > 5%, cost > 200% of 7-day baseline / tenant, eval pass rate drop > 5%.

### 9.10 CI/CD
- Eval suite gates release.
- Prompt-diff review required.
- Provider mock for unit + real provider in `staging`.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                  | Likelihood | Impact   | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Drafter hallucinates columns                                          | High       | High     | Validator deterministic; not trusted; resolver constrains.                                       |
| Cost runaway from chatty users                                        | Med        | High     | Per-tenant budgets + UX cap on session length.                                                   |
| Provider DPA breach (data retained)                                   | Low        | Critical | DPA review; classification routing; per-tenant overrides.                                        |
| Auto-apply pressure from PMs                                          | High       | Critical | ADR forbids; UX hard-codes human approval.                                                       |
| Prompt-injection bypass                                               | Med        | High     | Layered defenses; ongoing red-team.                                                              |
| Eval set rot                                                          | Med        | Med      | Quarterly refresh; regression alarms.                                                            |
| Provider model silently updated                                       | High       | Med      | Pin model IDs; CI detects drift via fingerprint.                                                 |
| Drafter produces semantically correct but ethically problematic policy| Med        | High     | Adversarial Reviewer + human approval; audit.                                                    |

### Rollback
- Feature flag per tenant for AI draft.
- Per-prompt-version rollback.
- Per-provider failover toggle.

### Future Extensibility
- Multi-turn drafting (`conversationId`).
- Voice authoring.
- Adversarial Reviewer always-on (V2).
- Policy-template retrieval (semantic search over prior policies).
- Fine-tuned models per tenant (Phase 13+).

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] `apps/ai-orchestrator` deployed with PAP graph.
- [ ] All 8 nodes implemented + tested.
- [ ] Constrained decoding per provider.
- [ ] Per-tenant token + cost budgets.
- [ ] Admin console UX: streaming graph, JSON editable, simulator preview, approval surface.
- [ ] Eval-set + CI gate.
- [ ] OTel + cost telemetry.

### Acceptance Criteria
- [ ] NL prompt → drafted policy in editor within 10 s.
- [ ] Drafted policy passes Zod validation ≥ 99% of runs.
- [ ] Simulator preview alongside every draft.
- [ ] Approval persists to `policies` and invalidates caches.
- [ ] Prompt-injection refusal ≥ 99% on adversarial corpus.
- [ ] Average session cost < $0.10.

---

## 12. Production Readiness Checklist

- [ ] Provider failover tested.
- [ ] Per-tenant cost dashboards + alerts.
- [ ] Prompts versioned + change-management documented.
- [ ] DR runbook: provider outage, orchestrator crash, eval set rot.
- [ ] Legal sign-off on DPA tier per provider.
- [ ] Red-team report + remediation tracker.

---

## 13. Remaining Risks Carried Forward

- **Multi-turn drafting** absent.
- **Adversarial Reviewer** may be Phase 9.5 if budget constrained.
- **PEP graph (Phase 10)** completes the AI story.
- **Tenant-specific fine-tuned models** not yet supported.
- **Voice authoring** absent.
- **Eval-set scaling** (10k cases) deferred.
