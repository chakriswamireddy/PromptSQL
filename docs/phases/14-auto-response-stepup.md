# Phase 14 — Auto-Response, Step-Up Auth, Break-Glass

> **Duration:** 34–36 weeks (≈2 weeks focused) &nbsp; · &nbsp; **Owner:** Backend + Security &nbsp; · &nbsp; **Dependencies:** Phases 0–13
> **Companion:** [`../implementation-plan.md` §Phase 14](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Elevated risk now triggers **automatic response**:
- 71–85 → require step-up MFA before the next query.
- 86–95 → auto-mask additional columns mid-stream.
- 96–100 → block + force re-auth + page the tenant's security contact.

**Break-glass** delivers controlled, time-boxed bypass for emergencies — two human approvers required, all actions force-audited, max 1 hour with auto-revocation.

**Business rationale:** detection without response is theater. Auto-response is what converts "look, we found something suspicious" into "and we stopped it before damage." Break-glass converts "the platform is brittle" into "the platform protects you AND lets you act when seconds matter."

---

## 2. Scope Boundaries & Ownership

**In scope**
- Tenant-configurable auto-response playbooks tied to risk tiers.
- Step-up MFA flow + obligation token + retry semantics.
- Mid-flight masking and stream termination at the proxy.
- Break-glass workflow: dual approval, time-boxed, audited, opt-in policies.
- Admin console additions: active break-glass page, playbook editor, risk history.
- Customer-facing webhook events for `risk.spike`, `breakglass.activated`, `step_up_required`.

**Out of scope**
- ML-driven response optimization (deferred).
- Cross-tenant sharing of attack signatures (deferred).
- Auto-rotation of credentials (deferred).

**Ownership**
- **Drives:** Security Engineer + Backend Lead.
- **Reviews:** Product (UX), Compliance (break-glass semantics), Legal (auto-response liability).

---

## 3. Hard Dependencies & Sequencing

- Phase 13 risk score + score events.
- Phase 12 webhook fanout.
- Phase 6 proxy mid-stream control.
- Phase 2 MFA + `mfa_at` claim plumbing.
- Phase 3 PDP obligations machinery.

Sequencing: playbook schema → obligation tracker library → step-up flow → mid-flight masking → break-glass workflow → admin UI → tests + drills.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 14.1 — Auto-Response Playbooks

Per-tenant configurable, with defaults:

| Risk score range | Default action                                                       |
| ---------------- | -------------------------------------------------------------------- |
| 0–40             | Normal                                                               |
| 41–70            | Allow + tag for review (Live Activity highlight)                     |
| 71–85            | Require step-up MFA before next query (PDP returns obligation)       |
| 86–95            | Auto-mask additional columns (heightened protection)                 |
| 96–100           | Block + force re-auth + page tenant's `security_contact_email`/Slack |

Stored as `risk_playbooks(tenant_id, version, tiers jsonb, escalation_targets jsonb, active bool)`. Versioned + audited.

### 14.2 — Step-Up Auth Flow

When PDP returns a decision with obligation `require_mfa_within=5min`:

1. PEP (proxy or AI orchestrator) returns a structured error to the client:
   - HTTP: `401 step_up_required` with body `{obligation_token, idp_redirect_url, ttl}`.
   - PG wire: `ErrorResponse` with severity ERROR and a vendored SQLSTATE (`SX001`); the BI tool's user sees a clear prompt; modern drivers can be augmented with the platform's SDK to handle redirect.
2. Client redirects user to IdP step-up endpoint (carries `obligation_token` as `state`).
3. After MFA succeeds, IdP issues a new JWT with updated `mfa_at`.
4. Client retries with new token.
5. PDP sees fresh `mfaSince`; obligation satisfied; decision flips to permit.

`pkg/obligation` library used by all PEPs:
- Encodes/decodes obligation tokens (signed JWT, 5-min TTL).
- Tracks pending obligations per session.

### 14.3 — Mid-Flight Masking

When risk score escalates during a streaming query:

1. Risk-score event arrives at the proxy via Redis pub/sub.
2. Proxy intercepts the row stream:
   - If new tier requires extra masking → apply mask functions to remaining rows.
   - If new tier blocks → send `ErrorResponse` to client; call `pg_cancel_backend` on backend session; emit audit `access.terminated_mid_stream`.
3. Audit event includes pre- and post-event row counts.

This requires proxy to subscribe to per-user score updates (Phase 13's Redis stream).

### 14.4 — Break-Glass Workflow

Definition: time-boxed access bypassing specific policies for emergencies (incident response, regulator request, debugging).

Flow:
1. Initiator submits request in admin console: target user(s), scope (which policies to bypass), reason, requested duration (≤ 1 h, configurable down per tenant).
2. **Two human approvers required**, distinct from initiator. Both must MFA-fresh.
3. Approval grants a `break_glass_session(id, tenant_id, principal_id, scope, reason, started_at, expires_at, approvers[], status)`.
4. PDP and proxy enforce: for affected principals, `app.break_glass = true` is set in DB sessions; specific RLS policies that opt-in honor it via:
   ```sql
   USING (current_setting('app.break_glass', true) = 'true' OR <normal predicate>)
   ```
5. Every action under break-glass is audited with `metadata.break_glass = true` and mandatory reason.
6. **Auto-revocation** at TTL.
7. **Post-session summary**: a structured report to `tenants.security_contact` + super-admin within 24 h listing every action taken.

Policies that opt-in to honor break-glass declare it via `policies.allow_break_glass = true`. Default-deny: most policies do not honor it.

### 14.5 — Admin Console Additions

- **Active break-glass sessions** page (super-admin): list, terminate early, view audit.
- **Playbook editor** with simulator: "if user U had risk 85, what happens?"
- **Per-user risk history** page (already in Phase 13; extended with response-actions overlay).
- **Step-up event log** per session.
- **Security contact** config on tenant settings (email, Slack channel, PagerDuty integration).

### 14.6 — Customer Webhook Events

- `risk.spike` — score crossed a threshold.
- `step_up_required` — user prompted for MFA.
- `breakglass.activated` — break-glass session started.
- `breakglass.terminated` — early termination.
- `breakglass.expired` — auto-revocation.
- `auto_response.blocked` — user blocked by playbook.

### 14.7 — mTLS Client-Cert Auth (Long-Deferred from Phase 6)

For ML notebooks and CLI tools that don't handle interactive MFA gracefully:
- Issue per-user client certs via Vault PKI.
- Cert SAN encodes `user_id`, `tenant_id`, `mfa_at` (re-issued upon MFA).
- Proxy accepts mTLS in addition to connection-token auth.
- Step-up triggers cert re-issuance.

### 14.8 — Tests + Drills

- **Step-up:** simulate score crossing 71 → user prompted → after MFA, query succeeds.
- **Mid-flight mask:** start streaming 1M rows; mid-stream, increase score; verify mask applied to later rows.
- **Mid-flight terminate:** start streaming; trigger 96; verify clean termination + audit.
- **Break-glass:** two approvers required; auto-revocation works; opt-in policies honor; non-opt-in don't.
- **Race conditions:** score-update event arrives just as query starts; expected behavior verified.
- **Quarterly drill:** simulated security incident triggers break-glass; runbook validated.

---

## 5. Architectural Gaps & Missing Requirements

1. **Step-up UX for non-interactive clients.** Service accounts, ETL bots — step-up doesn't apply; design "trusted bot" path with stricter pre-grant controls.
2. **Obligation token storage.** Where does the client store it? Cookie? Redirect? Document per integration.
3. **PDP-PEP race conditions.** Decision cached before score event; mid-flight delivery is the safety net but design must consider both layers.
4. **Break-glass policy scoping language.** Which policies are bypassed must be unambiguous; reserve a DSL for scope specification.
5. **Approval routing.** Some tenants want approvals via Slack — Phase 12 webhooks help; design first-class integration.
6. **Recovery after auto-block.** A 96+ user is blocked; how do they recover? Define re-onboarding flow.
7. **Auto-response false-positive cost.** A blocked user during sales demo = enterprise embarrassment. Per-tenant default to *warning* before *blocking*.
8. **Customer escape valve.** Tenant admin must be able to pause auto-response per-user or globally (audited).
9. **Geographic / time-of-day overrides.** Some tenants want different playbooks for off-hours or geo.
10. **Privacy of step-up reasons.** Don't leak which column was the trigger to the BI tool's UI — generic reason only.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Step-up MFA times out (flaky network)                             | Generous retry window (5 min default); user can re-initiate.                                     |
| Risk-score event arrives after query finished                     | No mid-flight action; future queries affected.                                                   |
| Break-glass approver is the user themselves                       | Schema prevents; two distinct approvers required.                                                |
| Break-glass expires mid-incident                                  | Re-approval required; document SLA expectation.                                                  |
| Customer's security contact email bounces                         | Fall back to in-app banner + Slack webhook if configured.                                        |
| Auto-block during a quarterly board demo                          | Tenant pause-auto-response flag; admin sees prominent banner when paused.                        |
| Multiple risk events queue                                        | Idempotent per `(user, score_tier, time_window)`; dedupe within 60 s.                            |
| ML notebook can't handle redirect                                 | mTLS cert path; step-up triggers cert re-issuance with `mfa_at` stamp.                            |
| Step-up loop (user can't satisfy obligation)                      | Bound retries; escalate to security contact.                                                     |
| Auto-mask changes column types unexpectedly                       | Document column-mask semantics per type; conservative defaults.                                  |
| Break-glass scope too broad                                       | Validator rejects scopes wider than tenant policy allows.                                        |
| Stream-cancellation backend left in odd state                     | Connection forcibly closed + sweeper reclaims.                                                   |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Per-tenant playbook evaluation is O(1).
- Mid-flight intervention adds minimal overhead (Redis pub/sub).
- Break-glass concurrency low-volume by nature.

### 7.2 Security
- Break-glass is high-leverage; every action audited and reported.
- Step-up obligation tokens signed; replay detection.
- Auto-response decisions immutable; cannot be retroactively edited.
- Per-tenant security contact verified before going live.

### 7.3 Multi-Tenant Isolation
- Per-tenant playbooks; isolated.
- Per-tenant security contacts.
- Break-glass scoped per tenant.

### 7.4 Concurrency
- Idempotent risk-event processing.
- Two-of-N approval atomic via DB transaction with row locking.
- Stream cancellation safe under concurrent score updates.

### 7.5 Performance
- Step-up trigger latency p95 < 1 s.
- Mid-flight intervention latency p95 < 2 s after score event.
- Break-glass approval workflow < 30 s round-trip in UI.

---

## 8. Recommended Improvements

### Architecture
- Auto-response as a **rules engine** so customers can author custom playbooks.
- An **obligation tracker** library shared by every PEP.
- A **break-glass module** with its own audit hash chain (extra integrity).

### DX
- A `breakglass-cli grant --user --reason --duration` for super-admin emergencies (still requires UI approvers).
- Test harness for playbook tuning.
- Replay tool for "what would happen if this playbook were active?"

### UX
- Step-up prompts surface in the BI tool via the platform's SDK; for non-SDK paths, a clear error message + URL.
- Break-glass approval UI shows full context: who, what scope, why, how long, recent activity of target.
- Auto-response activity timeline per user.

### Reliability
- Health-aware playbook execution: degrade gracefully if score pipeline lags.
- Approval workflow has out-of-band fallback for emergencies (e.g., super-admin override with mandatory after-action review).

### Observability
- Dashboards: auto-response trigger rate per tenant, step-up success rate, break-glass usage rate.
- Alerts: auto-block events, break-glass activations, step-up failure spikes.

### Maintainability
- ADRs: auto-response model, break-glass policy scoping, mTLS cert lifecycle.
- Quarterly tabletop drill calendar.

---

## 9. Technical Considerations

### 9.1 DB Design
- `risk_playbooks` (above).
- `breakglass_sessions(id, tenant_id, principal_id, scope, reason, approvers uuid[], status, started_at, expires_at, terminated_at, summary jsonb)`.
- `breakglass_audit` with its own hash chain trigger.
- `step_up_obligations(id, tenant_id, session_id, reason, satisfied_at, created_at, expires_at)`.

### 9.2 API Contracts
- `POST /v1/breakglass/request`, `/approve`, `/terminate`.
- `GET /v1/breakglass/sessions/{id}`.
- `POST /v1/playbooks`, `GET`.
- `POST /v1/auth/step-up/initiate`, `complete`.

### 9.3 RBAC
- `breakglass.request`, `breakglass.approve`, `breakglass.terminate`.
- `playbook.read`, `playbook.write`.
- `step_up.initiate`.

### 9.4 Validation Flows
- Playbook tier ranges contiguous + non-overlapping.
- Break-glass scope strictly within tenant.
- Approvers distinct from initiator.

### 9.5 Caching
- Playbook cache 60 s with pub/sub invalidation.
- Obligation token verifier caches JWKS (existing).

### 9.6 Queues & Background Jobs
- Break-glass auto-revoker cron.
- Post-session summary generator.
- Auto-response idempotency dedupe.

### 9.7 Audit Logs
- Every auto-response decision audited with playbook version.
- Break-glass actions in dedicated chain.
- Step-up requests + completions audited.

### 9.8 Retry & Idempotency
- Risk-event processing idempotent by `(user, tier, window)`.
- Step-up initiation idempotent by token.

### 9.9 Monitoring
- Trigger rate, false-positive rate (from override events).
- SLO: auto-response decisions reflect within 2 s of risk event.

### 9.10 CI/CD
- Drill scenarios scripted; quarterly run mandatory.
- Step-up flow integration tested with IdP mock.
- Break-glass approval workflow regression tests.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                              | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Step-up timeout during flaky network                              | Med        | Med      | Generous retry windows; document.                                                                |
| Auto-block during demo                                            | Med        | High     | Tenant pause + default to warn-before-block; admin banner.                                       |
| Break-glass abuse                                                 | Med        | Critical | Two approvers + audit + quarterly review.                                                        |
| ML notebook unsupported step-up                                   | High       | Med      | mTLS path + clear docs.                                                                          |
| Race condition between decision cache and risk update             | Med        | High     | Mid-flight intervention as belt-and-suspenders.                                                  |
| Stream-cancellation leaves stale connection                       | Low        | Med      | Sweeper reclaims; tested.                                                                        |
| Auto-response liability                                           | Med        | High     | Legal review; default to least-disruptive; warn-before-block default.                            |

### Rollback
- Per-tier feature flags.
- Tenant pause-all auto-response flag.
- Break-glass system feature flag.

### Future Extensibility
- ML-driven playbooks (learn from override patterns).
- Customer-authored playbook DSL.
- Auto-revoke API keys / sessions on extreme risk.
- Cross-tenant attack-signature sharing (opt-in).

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Playbook schema + admin UI + simulator.
- [ ] Step-up MFA flow with obligation tokens.
- [ ] Mid-flight masking + termination in proxy.
- [ ] Break-glass dual-approval workflow + auto-revocation.
- [ ] mTLS client-cert auth path.
- [ ] Customer webhook events.

### Acceptance Criteria
- [ ] Score 71 crosses → next query prompts MFA.
- [ ] Score 86 during streaming → mid-flight mask intensifies.
- [ ] Score 96 → block + page security contact.
- [ ] Break-glass requires 2 approvers + auto-expires + emits summary.
- [ ] Customer pause flag halts auto-response within 5 s.

---

## 12. Production Readiness Checklist

- [ ] Quarterly drill scheduled.
- [ ] Legal review of auto-block liability.
- [ ] Customer escape-valve docs.
- [ ] Step-up integration tested with each supported BI tool.
- [ ] Break-glass runbook + tabletop exercise.
- [ ] Security-contact validation per tenant.

---

## 13. Remaining Risks Carried Forward

- **ML-driven playbook tuning** deferred.
- **Customer-authored playbook DSL** deferred.
- **Cross-tenant attack-signature sharing** deferred (privacy-sensitive).
- **Auto-rotate credentials on extreme risk** deferred.
- **End-to-end traceability** of step-up across browser ↔ IdP ↔ proxy depends on the BI tool's cooperation.
