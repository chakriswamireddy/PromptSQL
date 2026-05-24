# Phase 13 — Anomaly Detection & Risk-Aware ABAC

> **Duration:** 30–34 weeks (≈4 weeks focused) &nbsp; · &nbsp; **Owner:** ML Engineer + Backend &nbsp; · &nbsp; **Dependencies:** Phases 0–12
> **Companion:** [`../implementation-plan.md` §Phase 13](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Every user has a continuously updated **risk score** (0–100) computed from their recent behavior. The score becomes a **first-class ABAC variable** that policies reference (`subject.riskScore < 70`). Phase 14 then automates response (step-up, masking, termination) keyed off the score.

**Business rationale:** static RBAC catches everything obvious. UEBA-style behavioral analytics catch the unobvious — compromised credentials, insider threats, anomalous access patterns. Selling UEBA-equivalent capability as a built-in feature (rather than a $250K/year vendor) is differentiating. Risk-as-a-policy-variable lets customers express "no PII access when risk is elevated" declaratively.

---

## 2. Scope Boundaries & Ownership

**In scope**
- **V1 (this phase): Statistical baselines.** Per-user 90-day baseline; z-score outliers feed risk.
- **V2 (next quarter, optional in this phase):** Transformer + GNN ensemble for sequence + graph signals.
- Apache Flink (or Kafka Streams) pipeline consuming `audit.access.{env}`.
- `risk.scored` topic + per-user current score in Redis (TTL 60 s) for PDP access.
- PDP integration: `subject.riskScore` field in SessionContext, evaluated in policies.
- Calibration controls: warm-up, decay, allowlists, per-tenant tuning UI.
- Risk score visibility: user profile page + Live Activity coloring + alerts.
- Customer API to pull risk scores into their SIEM.

**Out of scope**
- Auto-response (Phase 14).
- Full ML model training infra (V2 territory).
- Cross-tenant federated learning (future).

**Ownership**
- **Drives:** ML Engineer.
- **Reviews:** Security (false positives, alert design), Backend (PDP integration), Product (calibration UX).

---

## 3. Hard Dependencies & Sequencing

- Phase 5 Kafka stream of `audit.access.*`.
- Phase 3 PDP with extension point for new SessionContext fields.
- Phase 12 live feed + webhooks.
- Phase 7 schema catalog (for resource sensitivity weighting).

Sequencing: V1 baseline pipeline → score writer → PDP integration → policy variable wiring → admin UI surfacing → calibration → V2 (deferred ensemble).

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 13.1 — Detection Approach (Wave V1)

**Statistical baselines** per user, rolling 90 days:
- Hour-of-day distribution (24-bin histogram).
- Day-of-week distribution.
- Resources touched (set; track Jaccard distance from baseline).
- Row volume per query (log-distributed quantiles).
- IP / device fingerprint stability.
- Data-source mix.

Z-score outliers across multiple dimensions contribute weighted to risk score. Each contribution decays exponentially (half-life 1 h to start). Score capped 0–100.

**Why V1 statistical:** ship-able in 3 weeks, explainable to customers, low false positive when tuned, no GPU cost.

**V2 (later):** transformer for sequence + GNN for (user, resource) bipartite graph + risk-score regression.

### 13.2 — Streaming Pipeline

Apache Flink job consumes `audit.access.{env}`:

```
[Kafka source]
  → [Key by user_id]
  → [State: user_baseline (90d rolling histogram)]
  → [Feature extraction per event]
  → [Score per event via baseline z-score]
  → [Aggregation: per-event score + decayed total]
  → [Kafka sink: risk.scored.{env}]
  → [Redis sink: latest score per (tenantId, userId) with TTL 60s]
```

State backend: RocksDB; checkpointed to S3 (Phase 15 multi-region).

Why Flink over Kafka Streams: stateful aggregations at scale + exactly-once semantics + easier ML model integration in V2.

### 13.3 — Score Schema

```json
{
  "tenant_id": "...", "user_id": "...",
  "score": 73,
  "components": {
    "time_of_day": 0.4,
    "resource_volume": 0.2,
    "ip_drift": 0.3,
    "resource_novelty": 0.1
  },
  "decayed_total": 73,
  "computed_at": "...",
  "model_version": "stat-v1.2.0"
}
```

### 13.4 — PDP Integration

PDP loads `riskScore` for the current user from Redis at decision time (cached 60 s in L1, single-flight on miss). Adds to `SessionContext.riskScore`. DSL policies now reference it:

```json
{
  "all": [
    { "field": "subject.riskScore", "op": "lt", "value": 70 },
    { "field": "resource.classification", "op": "in", "value": ["public", "internal"] }
  ]
}
```

A user with elevated risk score automatically loses access to sensitive data without manual intervention.

### 13.5 — Risk-Score Visibility

Admin Console additions:
- User profile page: current score + 30-day history sparkline + last spikes with reason.
- Live Activity feed: color by score (green < 40, yellow 41–70, orange 71–85, red 86+).
- Alerts page: score spike events for a tenant; ack workflow.

Customer-facing API: `GET /v1/users/{id}/risk-score` and `GET /v1/risk/events` for SIEM pull.

### 13.6 — Calibration & Tuning

False positives kill UEBA products. Controls:
- **Warm-up:** score = "unknown" for first 30 days per user. Policies referencing `subject.riskScore` default to "low" during warm-up unless flagged otherwise.
- **Cooldown / decay:** exponential decay (half-life 1 h) prevents a single spike from sticking.
- **Allowlists:** known maintenance windows, batch jobs, service accounts.
- **Per-tenant UI:** show score distribution histogram, sliders for per-component weight, threshold editor.
- **Cohort fallback:** small tenants with too little data fall back to role-level or team-level baselines.

### 13.7 — Resource Sensitivity Weighting

Z-scores alone aren't enough — access to `restricted` data weighed more than access to `public`. Multiply event score by `1 + classification_weight` where weights are:
- `public`: 0.0
- `internal`: 0.2
- `confidential`: 0.5
- `restricted`: 1.0

### 13.8 — Risk-Score Replay

For incident review:
- All inputs deterministic (audit events + baselines + model versions).
- Reproduce a user's score history from a given point in time using cold storage.
- Used for forensics + customer support.

### 13.9 — V2 (Optional / Next Quarter)

- **Transformer** on user's recent access sequence.
- **GNN** on (user, resource) bipartite graph for "who else accesses this together."
- **Ensemble** weighted with V1 baselines.
- Training infra: feature store (Feast), model registry (MLflow), batch + online inference.
- Per-tenant fine-tuning if data is sufficient.

V2 differentiator; reserve in roadmap.

### 13.10 — Tests

- **Synthetic:** generate baselines + inject anomalies; verify score response.
- **Calibration:** replay 30 days of a real tenant's data; tune until false-positive rate < 5%.
- **PDP integration:** score in Redis → policy decision changes accordingly.
- **Determinism (replay):** same events twice → same score series.
- **Performance:** Flink lag < 5 s p95 at 10k events/sec.

---

## 5. Architectural Gaps & Missing Requirements

1. **Baseline cold-start.** New tenants / users have no baselines; cohort baseline + warm-up flag the only recourse.
2. **Concept drift.** Baselines must adapt to legitimate behavior changes (job change, team rotation). Half-life on baselines themselves.
3. **Multi-account users.** A consultant works across tenants; scores per tenant only (no cross-tenant aggregation).
4. **Service accounts.** Bots and ETL service accounts will trigger anomalies without allowlist; policy-aware allowlist tooling.
5. **Resource clustering.** A "new" resource is anomalous if it's actually similar to old ones — vector similarity reduces false positives.
6. **Explainability requirements.** SOC 2 / regulators want to know "why was this user blocked?" — components + reasons required.
7. **Adversarial robustness.** Attackers learn baselines and stay within them; design adversarial drills.
8. **Risk-score override.** Trusted admins should be able to manually lower a user's score (audited).
9. **Cost forecasting.** Flink state can grow large; per-tenant state size monitoring + alarms.
10. **Score TTL semantics.** Redis TTL vs Flink state — define authoritative.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Flink job crash → no scores                                       | Checkpoints + restart; PDP falls back to last cached score; if stale > 5 min, defaults to "unknown" + alert. |
| User's behavior legitimately changes (promotion)                  | Decay + cohort baseline; admin can flag + reset baseline.                                        |
| Service account triggers anomaly                                  | Allowlist + per-account baseline.                                                                |
| Risk score Redis outage                                           | PDP serves stale L1 cache; if absent, treats as "unknown" with conservative policy fallback.    |
| Replay produces different score after model change                | Always tag results with `model_version`; never compare across versions silently.                  |
| Tenant with one user → baseline meaningless                       | Cohort fallback to role/team or platform-wide stats.                                              |
| Concept drift accumulates over 1 yr                               | Quarterly baseline rebuild option; surface in UI.                                                |
| Adversary impersonates a user with known baseline                 | Combine with device + IP trust signals (Phase 14 enrolls device).                                |
| Score spike during deploys                                        | Allowlist deployment windows (admin-defined).                                                    |
| User triggers warm-up by inactivity                               | Pause warm-up if user inactive for > 30 days; resume.                                            |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- Flink horizontally scaled; key-by user_id partitions state.
- Per-tenant state size monitored; large tenants get dedicated Flink slot.
- Redis sized for 1M+ keys per region.

### 7.2 Security
- Risk-score endpoint authenticated; per-tenant ACL.
- Risk-score data treated as sensitive (`confidential`); admin views audited.
- Customer SIEM pull rate-limited + IP-allowlisted.

### 7.3 Multi-Tenant Isolation
- Per-tenant state in Flink; never cross.
- Per-tenant calibration UI.
- Per-tenant baselines never shared.

### 7.4 Concurrency
- Flink exactly-once for state updates.
- Redis writes idempotent.
- PDP single-flight on score fetch.

### 7.5 Performance
- Event-to-score latency p95 < 5 s.
- Score-to-PDP-visibility latency p95 < 6 s.
- PDP decision latency unaffected (still < 5 ms p99).

---

## 8. Recommended Improvements

### Architecture
- **Feature store** (Feast / Tecton) when V2 ML lands; structure features now to ease the transition.
- **Model registry** (MLflow) for V1 model + V2 ensemble; lifecycle managed.
- **Backfill tool** to recompute scores after model upgrades.

### DX
- `risk-cli replay --user --from --to` reproduces a user's score history.
- `risk-cli explain --user` returns current score with component breakdown.
- Local Flink dev compose for offline iteration.

### UX
- "Why is this user high risk?" panel with components, history, and last anomalous events.
- Per-tenant tuning UI: histogram, sliders, simulate.
- Score timeline overlay on Live Activity feed.

### Reliability
- Multi-AZ Flink with checkpointing.
- Score fallback semantics documented (unknown vs stale vs default-low).
- Decommission flow: deprecating a model version requires backfill.

### Observability
- Per-tenant false-positive rate dashboard.
- Score distribution histograms per tenant.
- Component contribution dashboards.

### Maintainability
- ADR: V1 statistical vs V2 ML; Flink choice; baseline half-life.
- Model versioning + change-management process.
- Eval set: labeled past incidents.

---

## 9. Technical Considerations

### 9.1 DB Design
- `risk_scores(tenant_id, user_id, score, components jsonb, computed_at, model_version)` — partitioned by day; Redis is the hot store.
- `risk_allowlists(tenant_id, principal_id, reason, until_at)`.
- `risk_calibrations(tenant_id, weights jsonb, thresholds jsonb, version, created_by, created_at)`.
- `risk_events(tenant_id, user_id, kind ∈ {spike, decay, override}, payload, created_at)` — feeds Live Activity + webhooks.

### 9.2 API Contracts
- `GET /v1/users/{id}/risk-score`.
- `POST /v1/users/{id}/risk-override`.
- `GET /v1/risk/events`.
- `GET /v1/risk/calibration` / `PUT`.

### 9.3 RBAC
- `risk.read`, `risk.override`, `risk.calibrate` (super-admin), `risk.replay`.

### 9.4 Validation Flows
- Calibration weight sum must equal 1.
- Threshold ranges sanity-checked.
- Allowlist principals must exist in tenant.

### 9.5 Caching
- Redis is the hot path (TTL 60 s).
- PDP L1 cache for risk reads.

### 9.6 Queues & Background Jobs
- Backfill worker on model upgrade.
- Quarterly baseline rebuild option.
- Per-tenant cost dashboards rolled up nightly.

### 9.7 Audit Logs
- Score spikes (≥ 70) emitted as `risk.spike` to Phase 12 webhooks.
- Manual overrides audited.
- Calibration changes audited.

### 9.8 Retry & Idempotency
- Flink exactly-once; Redis writes idempotent.
- Override API idempotent on key.

### 9.9 Monitoring
- Pipeline lag, state size, Redis size, false-positive rate (vs labels).
- Alerts: lag > 60 s, FP rate > 10% / 7 days, model drift signals.

### 9.10 CI/CD
- Synthetic pipeline test in CI.
- Eval set with labeled events; regression alerts.
- Backfill rehearsed monthly.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                              | Likelihood | Impact   | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Small tenants have too little data                                | High       | Med      | Cohort fallback + warm-up.                                                                       |
| False positives block legitimate work                             | High       | High     | Calibration UI + override + cooldown.                                                            |
| Adversaries learn baselines                                       | Med        | High     | Multi-factor signals; Phase 14 step-up; quarterly red-team.                                       |
| Concept drift accumulates                                         | Med        | Med      | Decay + rebuild option.                                                                          |
| Pipeline outage drops scores silently                             | Low        | High     | Score fallback to "unknown"; alarms.                                                              |
| Flink state explodes                                              | Med        | High     | State monitoring; per-tenant cap; dedicated slot for big tenants.                                |
| Risk-score endpoint abused                                        | Med        | Med      | Rate limit + auth.                                                                                |
| Customer expects perfect detection                                 | High       | Med      | Set expectations explicitly; document V1 limitations.                                            |

### Rollback
- Feature flag for risk-score evaluation in PDP per tenant.
- Per-policy flag for `riskScore` reference.
- Pipeline rollback via Flink savepoint.

### Future Extensibility
- V2 ML ensemble.
- Cross-signal fusion (device + IP + behavior).
- Per-tenant fine-tuning.
- Federated learning across tenants (with strict isolation).
- Active learning loop using admin overrides as labels.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] Flink pipeline running statistical baseline.
- [ ] `risk.scored` topic + Redis sink.
- [ ] PDP integration with `subject.riskScore`.
- [ ] Admin UI: risk per user + calibration.
- [ ] Customer API to pull scores.
- [ ] Allowlists + warm-up + decay implemented.

### Acceptance Criteria
- [ ] Score computed for every access event within 5 s p95.
- [ ] PDP successfully uses `riskScore` in policies.
- [ ] False positive rate < 5% on a 30-day calibration run with a real tenant.
- [ ] Per-tenant calibration UI demonstrably reduces FP.
- [ ] Replay reproduces a user's history exactly.

---

## 12. Production Readiness Checklist

- [ ] Capacity model for Flink state.
- [ ] Multi-AZ checkpointing.
- [ ] Score-fallback semantics documented + alarms tested.
- [ ] Customer-facing risk API docs.
- [ ] Privacy review: risk data is sensitive.
- [ ] Calibration drill: tune for a synthetic tenant.

---

## 13. Remaining Risks Carried Forward

- **V2 ML ensemble** is the differentiator; V1 statistical is the floor.
- **Phase 14** layers auto-response on top of risk score.
- **Cross-tenant federation** for shared adversary signals deferred.
- **GPU model serving** for V2 reserved for Phase 15+.
- **Privacy-preserving learning** (federated, DP) deferred.
