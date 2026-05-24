# Phase-Wise Execution Plans
## AI-Native Authorization & Retrieval Governance Platform

> Companion index to: [`../implementation-plan.md`](../implementation-plan.md), [`../architecture-v2.md`](../architecture-v2.md)
> Authoring lens: **Principal Engineer / Staff+ planning for enterprise production.**
> Each file in this folder represents a *shippable, dependency-ordered execution milestone*. No phase depends on a later phase. Every phase has a working demoable artifact, a defined acceptance bar, and an explicit gap/risk register.

---

## Phase Map

| #  | Phase File                                                  | Theme                          | Demoable Result                                                          | Hard Dependencies     |
| -- | ----------------------------------------------------------- | ------------------------------ | ------------------------------------------------------------------------ | --------------------- |
| 0  | [00-foundation.md](00-foundation.md)                        | Repo, infra, CI, observability | `make up` brings the stack up in <15 min                                 | —                     |
| 1  | [01-control-plane-database.md](01-control-plane-database.md)| Schema, RLS, migrations        | Schema deployed, RLS forced, seeded                                      | Phase 0               |
| 2  | [02-authentication-session.md](02-authentication-session.md)| AuthN, SessionContext, mTLS    | Every request carries verified SessionContext                            | Phases 0–1            |
| 3  | [03-pdp-v1.md](03-pdp-v1.md)                                | Policy Decision Point          | Deterministic permit/deny < 5ms p99                                      | Phases 0–2            |
| 4  | [04-admin-console-simulator.md](04-admin-console-simulator.md)| Admin UI + policy simulator    | Author JSON policy → simulate → approve                                  | Phases 0–3            |
| 5  | [05-audit-pipeline.md](05-audit-pipeline.md)                | Tamper-evident audit           | Events → WORM + ClickHouse < 60s                                         | Phases 0–4            |
| 6  | [06-pep-postgres-proxy.md](06-pep-postgres-proxy.md)        | PG wire proxy + Calcite        | Real BI tool queries enforced via rewrite                                | Phases 0–5            |
| 7  | [07-schema-catalog-crawler.md](07-schema-catalog-crawler.md)| Metadata + classification      | Columns classified, embedded, quarantined                                | Phases 0–6            |
| 8  | [08-permission-aware-retrieval.md](08-permission-aware-retrieval.md)| Allowed snapshots + RAG ACLs | RAG honours per-chunk permissions                                        | Phases 0–7            |
| 9  | [09-ai-pap-graph.md](09-ai-pap-graph.md)                    | NL → JSON policy authoring     | English request → drafted + simulated policy                             | Phases 0–8            |
| 10 | [10-ai-pep-graph.md](10-ai-pep-graph.md)                    | NL → safe SQL                  | End user chat → permitted results                                        | Phases 0–9            |
| 11 | [11-multi-database.md](11-multi-database.md)                | Cross-DB enforcement           | MySQL/MSSQL/Oracle/Snowflake/BQ/Mongo/Databricks                         | Phases 0–10           |
| 12 | [12-realtime-stream.md](12-realtime-stream.md)              | Live activity + webhooks       | Live feed < 2s; webhook fanout                                           | Phases 0–11           |
| 13 | [13-anomaly-risk-abac.md](13-anomaly-risk-abac.md)          | UEBA + risk as ABAC variable   | Risk score feeds PDP decisions                                           | Phases 0–12           |
| 14 | [14-auto-response-stepup.md](14-auto-response-stepup.md)    | Step-up, masking, break-glass  | Risk → MFA / masking / termination                                       | Phases 0–13           |
| 15 | [15-scale-multiregion.md](15-scale-multiregion.md)          | K8s, HA, active-active         | Region failover < 30 min RTO                                             | Phases 0–14           |
| 16 | [16-compliance-ga.md](16-compliance-ga.md)                  | SOC 2 Type II + GA             | Audited, pentested, GA live                                              | Phases 0–15           |

Total budget: ~48 weeks. Every phase ships behind a feature flag. Every phase has rollback and DR considerations.

---

## How To Read A Phase File

Each phase file follows this canonical layout:

1. **Phase Objective & Business Purpose** — what done means, why this milestone exists.
2. **Scope Boundaries & Ownership** — what is in/out, which team owns it, hand-off points.
3. **Hard Dependencies & Sequencing** — what must be live before this phase begins.
4. **Detailed Sub-Phases & Implementation Tasks** — concrete sequenced work, with engineering notes.
5. **Architectural Gaps & Missing Requirements** — what the high-level plan does not yet specify.
6. **Edge Cases & Failure Modes** — concurrency, partial failure, byzantine inputs.
7. **Non-Functional Concerns** — scalability, security, multi-tenant isolation, performance, concurrency.
8. **DX / UX / Reliability / Observability / Maintainability Improvements** — judgement-call recommendations.
9. **Technical Considerations** — DB design, API contracts, RBAC, validation, caching, queues, background jobs, audit, retry/idempotency, monitoring, CI/CD.
10. **Risks, Rollback & Future Extensibility.**
11. **Deliverables & Acceptance Criteria.**
12. **Production Readiness Checklist.**
13. **Remaining Risks Carried Forward.**

---

## Cross-Cutting Standards Applied To Every Phase

- **Feature flags** gate every new code path. Default-off in `prod` until cohort rollout.
- **Forward-only migrations.** Expand-contract for schema breaking changes.
- **Audit-everything.** No control-plane action lands without an immutable `policy_audit` row.
- **Tenant isolation is non-negotiable.** RLS + scoped roles + `SET LOCAL` discipline in every DB transaction.
- **Idempotency keys** on every mutating endpoint that crosses a network boundary.
- **Trace + metric + log** per service span; no service ships without OpenTelemetry from day zero.
- **Two-person rule** for production policy/role mutations and infra changes.
- **Capacity headroom** target: 3× steady-state. Performance budgets enforced in CI.

---

## Reading Order

If you are **executing the plan**: read in numerical order, treat each phase as a sprintable milestone.
If you are **reviewing risk**: read the *Risks*, *Gaps*, and *Remaining Risks* sections across all files first.
If you are **onboarding**: start with Phase 0–2, plus the architecture doc, then drop into the phase your team owns.
