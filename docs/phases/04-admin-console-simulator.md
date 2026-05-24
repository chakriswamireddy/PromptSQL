# Phase 4 — Admin Console v1 + Policy Simulator

> **Duration:** 7–9 weeks &nbsp; · &nbsp; **Owner:** Full-stack (Next.js + Go backend) &nbsp; · &nbsp; **Dependencies:** Phases 0–3
> **Companion:** [`../implementation-plan.md` §Phase 4](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Deliver the human surface of the platform: an admin console where stewards author JSON policies, simulate impact against real or synthetic subjects, and approve with a two-person rule. The simulator is the "killer feature" — it turns policy authoring from blind guessing into a transparent, reviewable workflow that compares draft vs. active and predicts blast radius.

**Business rationale:** every customer demo lives or dies on the simulator. Authorization platforms succeed when operators trust the consequences of their changes — and that trust comes from seeing the diff *before* deployment. The two-person rule + immutable versions + audit trail are direct SOC 2 / ISO 27001 controls.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Next.js 14+ admin console (App Router).
- Pages: login, tenants list (super-admin), users, roles, policies, policy editor (Monaco), simulator (spot + diff), audit trail, data sources.
- Approval workflow: drafts immutable, two-admin approval gated by config flag, atomic version activation.
- Server-side BFF (Backend-for-Frontend) in `apps/api-gateway` exposes admin endpoints.
- Reuse PDP `Validate` and the shared `policy-engine` library for the simulator.
- Audit trail UI reading from PG (Phase 5 swaps to ClickHouse).

**Out of scope**
- AI-powered policy drafting (Phase 9).
- Live activity feed (Phase 12).
- Risk score visibility (Phase 13).
- Customer-facing self-serve onboarding (Phase 16).

**Ownership**
- **Drives:** Full-stack lead.
- **Reviews:** Security (approval workflow), UX (Monaco + diff usability), Backend (BFF contracts).

---

## 3. Hard Dependencies & Sequencing

- Phase 2 SessionContext + login.
- Phase 3 PDP `Decide`, `BulkDecide`, `Validate`, `Explain`.
- Phase 1 `policies` versioning + `policy_audit` chain.

Sequencing: app skeleton → auth integration → policy list + viewer → Monaco editor → simulator (spot) → simulator (diff) → approval workflow → audit trail → polish.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 4.1 — App Skeleton

- Next.js 14, App Router, React Server Components for read-heavy pages.
- TanStack Query (client) for mutations + invalidation.
- shadcn/ui component library; design tokens shared from `packages/ui/tokens`.
- React Hook Form + Zod (`@hookform/resolvers`) for forms.
- Monaco Editor with JSON schema injection (Policy schema generated from `packages/shared-types`).
- Authentication via the Phase 2 OIDC flow; access token in memory, refresh in `HttpOnly` `SameSite=Strict` cookie.
- Per-request CSRF token via double-submit cookie.

### 4.2 — Core Pages

| Page                            | Purpose                                                                                            |
| ------------------------------- | -------------------------------------------------------------------------------------------------- |
| **Login**                       | OIDC redirect, MFA prompt if required                                                              |
| **Tenants** (super-admin)       | List, create, suspend; filter by plan, residency, status                                           |
| **Users**                       | List with filters; detail page shows roles, attributes, sessions, last login, status               |
| **Roles**                       | Tree view honoring parent hierarchy; create/edit/archive; show resolved permissions                |
| **Policies**                    | List with filters: status, action, resource, role; bulk archive                                    |
| **Policy editor**               | Monaco JSON editor with schema validation, autocomplete, inline lint, simulator launch              |
| **Simulator**                   | Spot check + diff mode; saved scenarios                                                            |
| **Audit trail**                 | Policy audit + access audit tabs; filterable                                                       |
| **Data sources**                | Register a managed DB with Vault-backed credentials reference                                       |
| **Classifications**             | View + override per column (steward workflow precursor to Phase 7)                                  |

All pages are tenant-scoped via URL: `/t/{tenantSlug}/...`. Super-admin pages live at `/admin/...`.

### 4.3 — Policy Editor UX

- Monaco initialized with the **Policy JSON Schema** generated from Zod via `zod-to-json-schema`.
- **Autocomplete** for column names sourced from `/api/v1/schema/{datasourceId}/columns` (filtered by what the editing admin can see).
- **Inline lint:**
  - DSL boundedness (depth ≤ 5, ≤ 256 nodes).
  - Unknown column / table references flagged.
  - Cross-tenant references rejected.
  - Conflicts with existing active policies highlighted.
- **Action buttons:** Save Draft, Validate, Simulate, Submit for Review.
- **Versioning panel** shows previous versions; one-click "open in compare" loads diff mode.
- Keyboard: ⌘S saves, ⌘⏎ submits, ⌘K toggles simulator.

### 4.4 — Simulator: Spot Check

- Subject: real user (autocomplete) or **synthetic persona** (saved JSON of `SessionContext.attributes`).
- Action + resource pickers driven by `data_sources` + `schema_metadata`.
- Result panel:
  - **Decision:** PERMIT / DENY with reason.
  - **Matched policies:** clickable list with `Explain` payload.
  - **Allowed columns** with mask indicators.
  - **Denied columns.**
  - **Row filter** rendered as human-readable predicate.
  - **Obligations** (e.g., `require_mfa`) with current satisfaction state.
- Implementation: calls PDP `Explain` against the **draft** policy set (via `Validate` with merged set).
- Underneath: simulator runs the same `packages/policy-engine` code path as the runtime PDP — zero divergence.

### 4.5 — Simulator: Diff Mode (Killer Feature)

- Pick: current active set vs. draft.
- For each saved persona + a sample of real users (per role, sample size 20):
  - Run both sets; diff the resulting decisions.
- Output: structured report.
  - "+3 columns now visible: `email_domain`, `phone_country`, `signup_source`."
  - "+200 row conditions now block: rows where `campus_id != 'hyd'`."
  - "Affected users (estimated): 47."
  - "New obligations triggered: `require_mfa_within=5m` for 12 users."
- **Blast radius widget** — colored severity badge with click-through to top affected users.
- **Diff is reproducible** — saves the report by hash, so a reviewer opens the same diff later.

### 4.6 — Approval Workflow

Lifecycle: `draft` → `pending_review` → `active` → `archived` (versioned).

- Drafts are mutable until submission; on submission they freeze.
- Reviewer must differ from author when `tenants.config.dual_approval = true`.
- **Atomic activation:** in a single transaction, the new version is marked `active`, the previous `archived`, a `policy_audit` row appended (hash-chained), and the Phase 3 pub/sub invalidation event published *after* the transaction commits (outbox pattern).
- **Outbox pattern:** the activation writes a row to `outbox_events`; a relay process publishes to Redis pub/sub and marks the row sent. Guarantees pub/sub event matches the committed state.
- Rollback = create a new version with prior content; same approval workflow applies.

### 4.7 — Audit Trail Page

Tabs:
- **Policy audit** — who changed which policy; before/after JSON diff; verifies hash-chain integrity.
- **Access audit** — who accessed what data (Phase 5 fills the volume).

Filters: user, resource, action, decision, date range, free-text on reason.

Detail drawer: full event payload, linked trace, rewritten SQL hash (Phase 6), risk score (Phase 13).

### 4.8 — Server-Side BFF

`apps/api-gateway` exposes admin endpoints:

| Endpoint                                        | Method | Notes                                                  |
| ----------------------------------------------- | ------ | ------------------------------------------------------ |
| `/api/v1/policies`                              | GET    | List with filters; cursor pagination                   |
| `/api/v1/policies/{id}`                         | GET    | Single + version history                               |
| `/api/v1/policies`                              | POST   | Create draft (idempotency-key required)                |
| `/api/v1/policies/{id}/submit`                  | POST   | Move draft → pending_review                            |
| `/api/v1/policies/{id}/approve`                 | POST   | Atomic activation                                      |
| `/api/v1/policies/{id}/archive`                 | POST   | Soft-archive                                           |
| `/api/v1/policies/simulate`                     | POST   | Spot mode                                              |
| `/api/v1/policies/simulate/diff`                | POST   | Diff mode; returns report id + body                    |
| `/api/v1/audit/policies`                        | GET    | Filterable                                             |
| `/api/v1/audit/access`                          | GET    | Filterable                                             |
| `/api/v1/users`, `/api/v1/roles`, `/api/v1/data-sources` | …    | Standard CRUD                                        |

All write endpoints require: idempotency-key, dual-approval check, RLS-correct tenant scoping, `policy_audit` row.

### 4.9 — Authoring Helpers

- **Templates library:** ship 30+ common policy templates (PII masking, region-based row filter, finance read-only, etc.). Customers clone-and-edit.
- **Schema-aware suggestions:** "you referenced `users.email` — typically classified PII; add `mask_email_domain`?"
- **Lint warnings, not just errors:** "this policy allows all columns; consider explicit allowlist." Non-blocking.

### 4.10 — Internationalization & Accessibility

- All copy via `next-intl`; English V1, ES/JA/DE reserved for Phase 16.
- WCAG AA: keyboard navigable, screen-reader labels, focus management on modal open/close, color contrast verified.
- High-density and low-density density modes for ops users.

---

## 5. Architectural Gaps & Missing Requirements

1. **Synthetic persona schema.** Define a portable, versioned JSON Schema for personas so they're shareable across tenants and CI.
2. **Diff sample size selection.** 20 users per role is heuristic; document and surface a per-tenant config.
3. **Real-user sampling consent.** Some tenants will object to running real users through simulator. Add a tenant toggle `simulator.real_user_sampling_enabled`.
4. **Pending-review TTL.** Drafts that linger forever clutter. Add 14-day auto-expire with notification.
5. **Concurrent edit collisions.** If two admins edit a draft simultaneously, who wins? Add ETag / version compare; reject stale writes with 409.
6. **Bulk operations.** Bulk archive of policies, bulk role assignment — need rate limits and bounded-cost previews.
7. **Approval ergonomics.** Many tenants want approval via Slack — defer to Phase 12 webhooks, but expose the `webhook` interface in Phase 4 as a stub.
8. **Search across policy bodies.** Full-text search over JSON conditions — implement via PG `to_tsvector` on flattened body. Heavy tenants may benefit from OpenSearch later.
9. **Export / import** of policies (JSON, YAML) for backup and cross-env promotion.
10. **Time-windowed activations.** `effective_from / effective_to` already in schema; UI must surface and warn on overlap.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Two admins edit the same draft                                    | ETag + optimistic concurrency; 409 with conflict-merge UI.                                       |
| Activation commits but pub/sub event is lost                      | Outbox pattern + 30s PDP poller (Phase 3) covers.                                                |
| Simulator runs against deleted user                               | Soft-delete preserved; UI marks "user deactivated"; results still reproducible.                   |
| Diff report runs against 50k users (huge tenant)                  | Cap sample size; offer "schedule full diff" as background job (Phase 12 queue).                  |
| Monaco editor pastes 10 MB JSON                                   | Client + server cap at 256 KB per policy body; reject with friendly error.                       |
| Browser tab lingers with stale token mid-edit                     | Refresh token rotation transparent; if refresh fails, save draft locally and prompt re-login.   |
| User loses MFA during approval                                    | Approval requires fresh MFA (`mfa_at` < 5 min); fall back to support escalation.                 |
| Hash-chain verifier flags `policy_audit` integrity break          | Hard ban on further mutations until incident review; admin console shows red banner.             |
| Simulator returns different result than PDP runtime               | Property test in CI; mismatch is P1 incident.                                                    |
| Approval clicked twice (double-submit)                            | Idempotency key + atomic update guarded by `WHERE status = 'pending_review'`.                    |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Admin console is low-RPS but high-importance. p95 latency budget < 500 ms.
- Diff mode is the heaviest endpoint; offload to a worker pool with concurrency limit per tenant.

### 7.2 Security
- All admin endpoints behind MFA-fresh requirement (≤ 5 min) for state-changing ops.
- CSRF + same-site cookies + CSP headers (`'strict-dynamic'`, nonce-based).
- SSRF defenses around webhook subscription input.
- Input validation on every form via Zod; sanitization of free-text fields.
- Markdown rendering (reasons, descriptions): hardened renderer with allowlisted tags.

### 7.3 Multi-Tenant Isolation
- URL-level tenancy enforced server-side: gateway rejects if path tenant ≠ JWT tenant unless super-admin.
- Cross-tenant search prohibited in code; tested.
- Simulator personas are tenant-scoped and never reused across tenants.

### 7.4 Concurrency
- Optimistic concurrency on policy edits; pessimistic locking on activation transactions.
- Outbox pattern ensures pub/sub aligns with committed state.

### 7.5 Performance
- p95 list page < 300 ms; p95 simulator spot < 800 ms; p95 diff (default sample) < 3 s.
- Diff results cached by `(draftHash, activeSetHash, sampleSize)` → reusable across reviewers.

---

## 8. Recommended Improvements

### Architecture
- Introduce **`policy_drafts`** as a separate table (or as a `status` partition) so drafts don't clutter the hot `active` path; activation moves the row.
- Outbox pattern table `outbox_events` with relay; reuse for Phase 5/12 event fanout.

### DX
- Storybook for components.
- Visual regression tests via Chromatic for key views.
- Per-PR preview deployments wired through Vercel/Netlify or self-hosted.
- A "Replay this decision" link in the audit trail opens the simulator with the exact inputs.

### UX
- Empty states with example policies + "try in simulator" CTAs.
- Side-by-side JSON + visual policy builder (drag-and-drop predicates) — V1 ships JSON; visual builder reserved for Phase 9.
- Color-coded severity in the diff blast radius widget; click drills to top affected users.
- "Why did this approval get blocked?" inline help.

### Reliability
- Optimistic UI for non-critical actions; pessimistic confirmations for activation and archive.
- Status indicators per page if the PDP/PG/Redis is degraded.

### Observability
- Frontend OTel via `@opentelemetry/sdk-trace-web`; spans correlate to server traces via `traceparent`.
- User-experience metrics: form-completion time, approval-cycle time, simulator usage funnel.
- Per-tenant audit-trail latency dashboard.

### Maintainability
- ADRs: BFF vs. direct client→PDP; outbox vs. transactional pub/sub; Monaco vs. CodeMirror.
- Strict TS configs (`strict`, `noUncheckedIndexedAccess`).
- Component-level test coverage gate.

---

## 9. Technical Considerations

### 9.1 DB Design
- `outbox_events(id uuidv7 pk, tenant_id, kind, payload jsonb, created_at, sent_at)` indexed on `(sent_at IS NULL, created_at)`.
- `policy_diff_reports(id, tenant_id, draft_hash, active_hash, body jsonb, created_by, created_at)` for cacheable diff results.
- `personas(id, tenant_id, name, attributes jsonb, owner_user_id)`.

### 9.2 API Contracts
OpenAPI 3.1; clients generated for TS via `openapi-typescript`. Idempotency keys on all writes. Cursor pagination.

### 9.3 RBAC
Admin console enforces *application-level* RBAC:
- `policy.read`, `policy.write`, `policy.approve`, `audit.read`, `tenant.manage`, `user.manage`, `role.manage`, `datasource.manage`.
- Permissions resolved from `roles` table; cached 60 s.
- Approval requires a `policy.approve` distinct from `policy.write`; the same user with both can still approve if `tenants.config.dual_approval=false`.

### 9.4 Validation Flows
- Client-side Zod validation for UX.
- Server-side re-validation (never trust client).
- DSL validation via PDP `Validate`.
- Cross-policy conflict detection during simulation diff.

### 9.5 Caching
- TanStack Query caches list views with optimistic invalidation.
- Diff results cached server-side (table above); 24-hour TTL.
- Tenant-config cached 30 s.

### 9.6 Queues & Background Jobs
- Diff jobs > sample size threshold → queued in Redis stream (BullMQ-like).
- Pending-review TTL sweeper (daily).
- `policy_audit` chain hourly verifier (Phase 5 takes over).

### 9.7 Audit Logs
Every state-change endpoint:
- Writes `policy_audit` row (hash-chained).
- Includes before/after snapshots, actor, IP, user agent, request id, trace id.
- Outbox publishes to Phase 5 pipeline.

### 9.8 Retry & Idempotency
- Mutating endpoints require `Idempotency-Key`; server stores key → result for 24 h to dedupe retries.
- gRPC retries to PDP only on idempotent calls.

### 9.9 Monitoring
- Frontend: Real User Monitoring (RUM) via OTel browser SDK.
- Backend: per-endpoint p95, error rate, payload size dashboards.
- Alerts: simulator failures > 1% / 5 min; approval failures > 0 / 5 min; outbox lag > 30 s.

### 9.10 CI/CD
- Storybook + Chromatic visual diff blocks regressions.
- Lighthouse CI gates accessibility + performance.
- Cypress (or Playwright) end-to-end suite: login → author → simulate → approve → audit verifies the whole loop.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                | Likelihood | Impact   | Mitigation                                                                                       |
| ------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Skipping the simulator to "save time"                               | Med        | Critical | Product PM owns the bar; no policy activation without a simulator preview in audit trail.        |
| In-place edits (instead of versioned writes)                        | Low        | Critical | Schema makes it impossible (`UPDATE policies SET conditions = …` denied to `app_write`).         |
| Approval workflow bypassed by a super-admin                         | Med        | High     | Even super-admins audited; `policy_audit` records `bypass_reason` mandatory.                     |
| Diff mode reveals cross-tenant data accidentally                    | Low        | Critical | Diff samples constrained to current tenant; tested.                                              |
| Monaco performance with very large policy sets                      | Med        | Med      | Cap policy size; split by editor file.                                                           |
| Webhook input becomes SSRF vector                                   | Med        | High     | URL allowlist + DNS pinning + private-network blacklist.                                         |
| Outbox relay falls behind                                           | Med        | High     | Worker scaling + lag alarm; Phase 5 dual-write helps.                                             |

### Rollback
- Feature flags per page (e.g., `feature.simulator.diff`); gradual rollout.
- Backwards-compatible API contracts; v1 endpoints supported for ≥ 2 releases.
- Policy version archive enables instant "create new version with prior content" rollback.

### Future Extensibility
- Visual policy builder lands in Phase 9 atop the same JSON model.
- Slack approval integrations land in Phase 12 via webhooks.
- AI-assist popovers (drafting, explanations) wire in Phase 9 without rebuild.
- Mobile-friendly admin reads (read-only) reserved for future.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Admin console with all V1 pages live in `dev` and `staging`.
- [ ] Monaco-based policy editor with schema-aware autocomplete + lint.
- [ ] Simulator: spot + diff with persona library.
- [ ] Two-admin approval workflow with config flag.
- [ ] Atomic activation via outbox pattern.
- [ ] Audit trail page reading from PG (Phase 5 migrates to ClickHouse).
- [ ] Property test: simulator decisions match PDP runtime on a 1000-case corpus.

### Acceptance Criteria
- [ ] Admin authors a JSON policy, simulates, approves, and sees enforcement via PDP.
- [ ] Approval blocked if same author = approver and dual-approval enabled.
- [ ] Diff mode produces stable reports across reruns with identical inputs.
- [ ] Concurrent edit yields 409, not silent overwrite.
- [ ] Every action writes a `policy_audit` row with valid hash chain.
- [ ] WCAG AA + Lighthouse perf ≥ 90 on critical pages.

---

## 12. Production Readiness Checklist

- [ ] CSP + HSTS + frame ancestors locked.
- [ ] CSRF tested with synthetic attacker requests.
- [ ] OWASP Top 10 review of admin endpoints.
- [ ] Penetration test scope includes admin console.
- [ ] Alerts on outbox lag, simulator failures, approval errors.
- [ ] Runbook: stuck approvals, broken hash chain, mass policy rollback.
- [ ] Customer onboarding doc: how to create your first policy, simulate, approve.

---

## 13. Remaining Risks Carried Forward

- **Audit trail still reads PG** — Phase 5 swaps to ClickHouse; until then, big tenants will see slow audit pages.
- **No live activity** — Phase 12 fills the realtime gap.
- **No AI drafting** — Phase 9 lands the NL → JSON flow.
- **No risk score display** — Phase 13 surfaces it.
- **Visual builder absent** — JSON only; some admins will object until visual tooling lands.
- **Cross-environment promotion** of policies is manual export/import; CI/CD for policies arrives in Phase 11+.
