# Phase 5 — Audit Pipeline & Tamper-Evident Logging

> **Duration:** 9–10 weeks (overlap with Phases 3–4) &nbsp; · &nbsp; **Owner:** Data Platform &nbsp; · &nbsp; **Dependencies:** Phases 0–4
> **Companion:** [`../implementation-plan.md` §Phase 5](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

Every policy change and every access decision flows end-to-end — producer → Kafka → ClickHouse (query) + WORM (compliance) — within 60 seconds, with the DB-side hash chain mirrored to immutable object storage and verified hourly. Audit is the substrate on which SOC 2 Type II, ISO 27001, anomaly detection (Phase 13), and customer trust are built.

**Business rationale:** customers buy *evidence*. "What happened?" must be answerable from any consumer (admin UI, SIEM webhook, regulator query) within seconds. Tamper-evidence is non-negotiable; without it the audit is not credible in a dispute.

---

## 2. Scope Boundaries & Ownership

**In scope**
- `packages/audit-client` producer SDK (Go + TS) with batching + bounded ring buffer + Prometheus metrics.
- Kafka topic design + producer config (idempotent, `acks=all`).
- Three consumers: `clickhouse-sink`, `worm-sink`, `chain-verifier`.
- ClickHouse schema + ingestion + retention.
- WORM bucket (S3/MinIO) with Object Lock Compliance mode.
- Hourly hash-chain verifier comparing PG `policy_audit.row_hash` with WORM digest.
- GDPR right-to-erasure flow via deterministic HMAC tokenization.
- Admin console audit page migrated from PG → ClickHouse.

**Out of scope**
- Real-time dashboards / live activity feed (Phase 12).
- Webhook subscriptions (Phase 12).
- Anomaly detection model (Phase 13).
- SIEM-side integrations (Phase 16).

**Ownership**
- **Drives:** Data Platform Lead.
- **Reviews:** Security (WORM + chain integrity), Compliance (retention + GDPR), Infra (Kafka + ClickHouse).

---

## 3. Hard Dependencies & Sequencing

- Phase 1 hash-chain trigger + `access_audit` partition.
- Phase 4 admin UI for audit consumption.
- Phase 0 Kafka (dev compose), MinIO, ClickHouse.

Sequencing: producer SDK → Kafka contract → ClickHouse sink → WORM sink → chain verifier → admin migration → GDPR erasure flow → load + DR tests.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 5.1 — Producer SDK

`packages/audit-client` (Go + TS, identical interface):

```ts
audit.policyEvent({
  action: 'policy.create' | 'policy.activate' | 'policy.archive' | 'policy.simulate' | …,
  policyId, beforeState, afterState,
  metadata: { requestId, traceId, ip, userAgent, mfaAt }
});

audit.accessEvent({
  userId, tenantId, dataSourceId, resource, action,
  decision, reason, rowCount, queryHash,
  durationMs, riskScore?, breakGlass?,
  metadata: { … }
});
```

Behavior:
- Local batch buffer flushes every 500 ms or 1 MB, whichever first.
- Idempotent producer (`enable.idempotence=true`) + `acks=all` + `compression=zstd`.
- On Kafka outage: spool to local disk (size-bounded, e.g., 256 MB per pod, configurable). Emit `audit_buffered_total`. Hard ceiling drops with `audit_dropped_total` + page.
- All events carry `event_id` (UUIDv7), `tenant_id`, `event_time`, `service`, `version`.
- Schema-versioned: every event has `schema: "v1"`; consumers branch on version.

### 5.2 — Kafka Topic Design

| Topic                                | Partitions | Key       | Retention | Notes                                  |
| ------------------------------------ | ---------- | --------- | --------- | -------------------------------------- |
| `audit.policy.{env}`                 | 24         | tenant_id | 7 days    | Partition by tenant for ordering        |
| `audit.access.{env}`                 | 96         | user_id   | 7 days    | High-volume; tenant-prefix in payload  |
| `audit.system.{env}`                 | 6          | n/a       | 30 days   | Deploys, role grants, key rotations    |

Kafka is **transport, not storage** — long-term storage is ClickHouse + WORM.

Per-tenant ordering preserved by partition key choice. Cross-tenant ordering not required.

### 5.3 — ClickHouse Sink

Service `apps/audit-clickhouse-sink` (Go). Consumes both topics; uses ClickHouse async insert + bulk batching.

Schema:

```sql
CREATE TABLE audit_policy (
  event_id      UUID,
  tenant_id     UUID,
  actor_id      UUID,
  actor_token   String,           -- HMAC-tokenized for GDPR
  action        LowCardinality(String),
  policy_id     UUID,
  before_json   String,
  after_json    String,
  request_id    UUID,
  trace_id      String,
  ip            IPv6,
  user_agent    String,
  event_time    DateTime64(3, 'UTC'),
  ingest_time   DateTime64(3, 'UTC') DEFAULT now64()
) ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (tenant_id, event_time, event_id)
TTL toDate(event_time) + INTERVAL 7 YEAR;

CREATE TABLE audit_access ( … similar … )
PARTITION BY toDate(event_time)
ORDER BY (tenant_id, user_id, event_time);
```

- Materialized views for hot rollups: `decisions_per_tenant_per_hour`, `denies_per_user_per_day`, `query_latency_p99_per_datasource_per_5m`.
- Dictionaries for `action`, `decision`, `data_source_id` to keep storage compact.
- Per-tenant retention enforced via TTL expressions parameterized at table creation; tenants requiring longer retention get a dedicated database with custom TTL.

### 5.4 — WORM Sink

Service `apps/audit-worm-sink` (Go). Writes one object per `(tenant, topic, hour)` in MinIO (dev) / S3 (prod) with Object Lock Compliance mode + tenant-configurable retention years (default 7).

Object layout:

```
s3://audit-worm-prod/
  tenant=<uuid>/
    topic=policy/
      year=2026/month=05/day=20/hour=14/
        events-2026-05-20T14.jsonl.zst
        manifest.json    # hash chain end-of-hour + signature
```

`manifest.json` contains:
- Last `row_hash` from PG `policy_audit` for that tenant at end-of-hour.
- SHA-256 over the JSONL file.
- Producer service version.
- Signed via KMS (asymmetric) for tamper-detection on read.

Object Lock prevents physical delete; GDPR erasure relies on tokenization (5.7).

### 5.5 — Hash-Chain Verifier

Cron service `apps/audit-chain-verifier` runs hourly:
1. Reads last hour's `policy_audit` rows from PG (per tenant).
2. Recomputes hash chain.
3. Reads corresponding WORM manifest + JSONL.
4. Compares end-of-hour `row_hash`.
5. Mismatch → `audit_chain_mismatch_total` + PagerDuty page to Security.

Daily, runs a full-tenant pass on a random sample (10% of tenants). Quarterly, a complete full pass per tenant.

Verifier writes its own audit (`audit.system`) and stores results in a `chain_verifications` table for compliance evidence.

### 5.6 — Admin Console Migration

- Admin audit page switches from PG `policy_audit` reads to ClickHouse `audit_policy`.
- PG remains source of truth; ClickHouse is derived read model.
- Lag SLO: < 5 s p95.
- Fallback: on ClickHouse outage, admin UI falls back to PG with a banner.

### 5.7 — GDPR Right-to-Erasure

Two-phase semantics:

1. **Tombstone** the user in `users`: `status='deprovisioned'`, scrub `email`, `attributes`, `external_idp_subject` (preserve `id` for FK integrity).
2. **Tokenize** identifiers in audit:
   - Producer SDK already replaces `userId` with `userToken = HMAC(tenantKey, userId)` in event payloads.
   - For pre-existing rows in ClickHouse, run a backfill that recomputes `actor_token` and overwrites; raw `actor_id` columns get nulled where stored.
   - For WORM (no delete possible), destroy the per-tenant HMAC key → tokens become non-rejoinable to the user.
3. DPA documents that for WORM data the user becomes *pseudonymous-unlinkable* rather than physically deleted.

### 5.8 — Reliability & Backpressure

- Kafka consumer lag SLO: < 30 s p95; alert at 60 s.
- ClickHouse async insert buffer drains every 1 s or 64 MB.
- Dead-letter queue per consumer for poison messages (`audit.dlq.<consumer>`).
- Replay tool replays a DLQ partition after fix-forward.

### 5.9 — Load Test

- Sustained 50k events/sec across access + policy topics for 1 hour.
- p95 producer ack < 50 ms.
- ClickHouse ingest ≥ producer rate; storage growth predictable.
- WORM sink writes one object/hour/tenant under load.

---

## 5. Architectural Gaps & Missing Requirements

1. **Schema evolution.** Define the contract for adding fields (additive only) and for breaking changes (new `vN`). CI gate on `audit-schema breaking-change`.
2. **Multi-region audit.** WORM cross-region replication strategy not yet specified. Phase 15 territory but design now for it.
3. **Customer-managed keys for tokenization.** Some tenants will want their own KMS keys for actor tokenization. Schema must support per-tenant `audit_token_key_arn`.
4. **Per-tenant retention contracts.** Reflect in `tenants.config` and enforce automatically in ClickHouse TTL + WORM lifecycle.
5. **Search performance at scale.** Large tenants will request natural-language audit search (Phase 9 helps). Reserve ClickHouse skipping indices on `actor_token`, `resource`.
6. **Compliance taxonomy.** PCI-DSS, HIPAA, ISO require specific audit fields. Tag schema with `compliance_modes_required`.
7. **Sampling vs. complete audit for `permit` decisions.** At scale a 100% permit rate is expensive; design a sampling policy that's compliant (denies always 100%, permits sampled by configurable rate).
8. **SIEM integration spec.** A Splunk/Datadog/Elastic exporter for tenants. Phase 16 ships; document the contract now.
9. **Time-series gap detection.** A consumer outage may produce a gap that no one notices; add per-tenant heartbeat events emitted every 60 s and a gap-detector.
10. **Cross-event correlation.** Joining a policy change to the access decisions affected by it — design a `policy_version` foreign key in `audit_access` to enable.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Kafka cluster outage                                              | Producer disk buffer; alert > 60 s; consumer replay after recovery.                              |
| ClickHouse outage                                                 | Topic retention covers; consumer resumes on recovery; admin UI falls back to PG.                  |
| WORM bucket full / billing failure                                | Hard alarm at 80% capacity per tenant; lifecycle archives to Glacier Deep Archive.                |
| Producer dropped events under load                                | `audit_dropped_total` alarms; backpressure SLO defined; deny-write mode optional for highest-tier tenants. |
| Schema mismatch between producer + consumer                       | Schema registry (Confluent or Apicurio) + CI contract test.                                       |
| HMAC key destroyed accidentally                                   | Tokenization keys backed up to KMS with break-glass restore (within Object Lock period only).    |
| Hash-chain mismatch found                                         | Auto-page security; freeze further policy writes for tenant; runbook for forensic capture.       |
| GDPR erasure for actor still active                               | Refuse erasure on active actor; require deprovision first.                                       |
| Out-of-order events across consumers                              | Per-partition ordering preserved; cross-event-stream ordering not required.                       |
| Audit pipeline becomes a critical path for query latency          | Producer is fire-and-forget with bounded buffer; never blocks the query path.                    |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- ClickHouse handles billions of rows/day with proper partitioning; per-tenant database for top 10 tenants by volume.
- Topic partition counts sized for 5× projected peak.
- Async insert + materialized views keep query latency stable as data grows.

### 7.2 Security
- WORM bucket has Object Lock Compliance mode (cannot be overridden, even by root).
- Bucket policy denies `s3:DeleteObject`; only lifecycle transitions move objects between tiers.
- KMS-signed manifests verified on read; pipeline rejects tampered manifests.
- Kafka uses SASL/SCRAM + TLS in `staging`/`prod`; ACLs per-consumer-group.
- ClickHouse users least-privilege per consumer + per admin role.

### 7.3 Multi-Tenant Isolation
- Per-tenant HMAC keys in Vault for tokenization.
- WORM partitioned by tenant at top-level prefix.
- ClickHouse row policies via `clickhouse_access_policy` ensure admin queries don't cross tenants without authorization.
- ClickHouse cluster sized to keep noisy tenant from starving others; quotas per `tenant_id`.

### 7.4 Concurrency
- Idempotent producer + consumer offset commits exactly-once-ish; downstream consumers idempotent on `event_id`.
- Hash-chain verifier coordinates via PG advisory lock to prevent overlap.

### 7.5 Performance
- Producer ack p95 < 50 ms.
- Topic → ClickHouse end-to-end p95 < 5 s.
- Topic → WORM hourly batch by design.
- Verifier full-tenant pass < 10 min for typical tenant.

---

## 8. Recommended Improvements

### Architecture
- Adopt **schema registry** (Confluent or Apicurio) on day one; cheap to start, prohibitive to retrofit.
- Use **CDC from PG** (`wal2json` / Debezium) for `policy_audit` as an alternative producer path; the application-side SDK + CDC are belt-and-suspenders.
- Define an **outbox pattern** in the producing services so audit + business write are atomic — Phase 4 already includes `outbox_events`; extend it for audit.

### DX
- A `make audit-tail` CLI that streams events for a tenant.
- A `make audit-replay --topic --from --to` tool for re-running DLQ.
- Local Grafana dashboard preview with anonymized data.

### UX
- Admin audit page: virtualized table for millions of rows; saved filter sets; CSV/JSON export with rate limits.
- "Explain this event" drawer linking to traces, policies, and (Phase 13) risk score history.
- One-click "open in simulator" for any policy change event.

### Reliability
- Multi-AZ Kafka in prod (Phase 15 moves to MSK / Confluent Cloud).
- Cross-region WORM replication.
- Continuous backfill validator: re-derive a day's rollups and compare.

### Observability
- Dashboards per pipeline stage: producer success rate, Kafka lag, ClickHouse insert lag, WORM write success, verifier status.
- Tracing carries `event_id` so an audit row is joinable to its originating trace.
- SLOs published per consumer.

### Maintainability
- ADRs: ClickHouse vs. OpenSearch; WORM vendor lock-in; tokenization HMAC key lifecycle.
- Schema evolution doc with examples.
- Runbooks: Kafka outage, ClickHouse outage, WORM full, chain mismatch, GDPR erasure.

---

## 9. Technical Considerations

### 9.1 DB Design
- ClickHouse schemas above.
- New PG tables: `chain_verifications`, `tenant_audit_keys` (Vault references), `audit_dlq_replays`.

### 9.2 API Contracts
- Internal: producer SDK + Kafka topic schemas.
- Admin: `/api/v1/audit/access`, `/audit/policies` with cursor pagination + facet aggregation.
- Customer SIEM export contract drafted (NDJSON over HTTPS) for Phase 16 wiring.

### 9.3 RBAC
- Audit reads require `audit.read` permission; tenant-scoped.
- Audit replay / DLQ requires `audit.admin` (super-admin only).

### 9.4 Validation Flows
- Schema-registry compatibility check in producer SDK boot.
- CI test: replay a synthetic outage and verify zero loss on the producer disk-buffer path.

### 9.5 Caching
- Admin audit page caches facet counts 5 s per filter set.
- Materialized views serve common rollups.

### 9.6 Queues & Background Jobs
- All consumers run as deployments with HPA on consumer lag.
- DLQ replay is a manual job; tooling provided.
- Verifier scheduled (CronJob in K8s; cron in compose).

### 9.7 Audit Logs
The audit pipeline audits *itself*:
- `audit.system` events for verifier results, DLQ replay, key rotation, schema version change.
- Recursive self-audit avoids blind spots.

### 9.8 Retry & Idempotency
- Producer idempotent.
- Consumers idempotent on `event_id` (ClickHouse uses `ReplacingMergeTree` to dedupe; WORM dedup at object level via content hash).
- DLQ replay tools include idempotency key.

### 9.9 Monitoring
- SLO: 99.9% of events from producer to ClickHouse within 60 s.
- Alerts: producer drop > 0, consumer lag > 60 s, WORM write fail, chain mismatch.

### 9.10 CI/CD
- Schema-registry compatibility tests gate PRs.
- Synthetic load test in `staging` weekly.
- Quarterly DR drill: simulate Kafka cluster loss; verify recovery.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                | Likelihood | Impact   | Mitigation                                                                                       |
| ------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| WORM cost explosion under buggy producer flood                      | Med        | High     | Per-tenant daily volume alarm + producer disk-buffer ceiling.                                    |
| Object Lock unbreakable retention (legal headache)                  | Med        | Med      | Default retention 7 years; per-tenant configurable downward (subject to compliance constraints).|
| GDPR erasure complaint (token still data)                           | Low        | High     | DPA clarifies pseudonymous-unlinkable; legal review.                                              |
| ClickHouse cluster split-brain                                      | Low        | High     | Use ZooKeeper-less Keeper; per-shard backups; documented recovery.                                |
| Schema evolution breaks a consumer                                  | High       | Med      | Schema registry compatibility gate.                                                              |
| Audit pipeline becomes critical path                                | Med        | High     | Producer fire-and-forget; query path never blocks on audit.                                       |

### Rollback
- Producer SDK ships with `audit.enabled` flag (default on); off during incident to isolate.
- Consumer deployments rollback via flag; topic data preserved within 7-day retention.
- ClickHouse migrations forward-only; new columns nullable.

### Future Extensibility
- Real-time stream consumer (Phase 12) joins the pipeline as a new sink, no producer change.
- Anomaly detector (Phase 13) consumes `audit.access` directly.
- Webhook fanout (Phase 12) consumes `audit.*`.
- SIEM exporter (Phase 16) consumes ClickHouse with tenant API key.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] `packages/audit-client` SDK in Go + TS, integrated by all services.
- [ ] Kafka topics live in `dev`/`staging`/`prod`.
- [ ] ClickHouse sink with schemas + materialized views.
- [ ] WORM sink with Object Lock Compliance.
- [ ] Hash-chain verifier hourly + sampled daily + quarterly full.
- [ ] Admin console audit pages reading from ClickHouse.
- [ ] GDPR erasure flow tested end-to-end.

### Acceptance Criteria
- [ ] Policy change appears in ClickHouse < 60 s p95.
- [ ] WORM object written hourly per tenant + topic.
- [ ] Tampering with PG `policy_audit` detected by next verifier run.
- [ ] Producer disk buffer absorbs 1 h Kafka outage without drop.
- [ ] Synthetic GDPR erasure: user tokenized everywhere; WORM key destruction makes tokens unlinkable.
- [ ] Load test: 50k events/sec sustained.

---

## 12. Production Readiness Checklist

- [ ] Per-tenant retention configured + audited.
- [ ] WORM cross-region replication.
- [ ] DLQ replay tooling documented + drilled.
- [ ] Verifier alerting + on-call runbook.
- [ ] Compliance evidence pack drafted (chain verifications, retention configs, KMS key inventory).
- [ ] Pen-test scope includes audit endpoints.
- [ ] Cost dashboard per tenant for audit storage.

---

## 13. Remaining Risks Carried Forward

- **No realtime UI** until Phase 12.
- **No anomaly model** consumes the stream until Phase 13.
- **SIEM exporter** unwritten until Phase 16.
- **Multi-region active-active** for audit storage deferred to Phase 15.
- **Search at petabyte scale** may demand OpenSearch alongside ClickHouse; deferred unless customer demand emerges.
