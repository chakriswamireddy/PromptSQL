# Phase 12 — Real-Time Event Stream & Live Access Feed

> **Duration:** 28–30 weeks (≈2 weeks focused) &nbsp; · &nbsp; **Owner:** Backend (streaming) &nbsp; · &nbsp; **Dependencies:** Phases 0–11
> **Companion:** [`../implementation-plan.md` §Phase 12](../implementation-plan.md)

---

## 1. Phase Objective & Business Purpose

The Kafka stream of every access decision becomes live: admins watch a **Live Activity** feed in real time, customers subscribe to **webhooks** for events (`policy.changed`, `access.denied`, `risk.spike`, `schema.drift`, `breakglass.activated`), and the platform is ready for Phase 13's anomaly detector to consume the same stream.

**Business rationale:** "show, don't tell" — when admins see access events landing in milliseconds, the platform stops being abstract and becomes a security operations surface. Webhook subscriptions deliver bi-directional integration with SIEMs, ITSM, and customer-specific workflows — a checklist requirement for every Enterprise sale.

---

## 2. Scope Boundaries & Ownership

**In scope**
- Kafka topic finalization (still single-broker in V1; Phase 15 → cluster).
- Producer reliability: idempotent, `acks=all`, disk buffer.
- Consumers: `clickhouse-sink` (existing), `worm-sink` (existing), `live-feed-broadcaster` (new), `webhook-fanout` (new), `anomaly-detector` (stub for Phase 13).
- Admin console: **Live Activity** WebSocket-driven feed.
- Webhook subscription management UI + HMAC signing + retries + DLQ.
- Per-tenant rate limits on webhooks.
- Saved-query scheduling (a small wrinkle into this phase since Phase 10 reserved it).

**Out of scope**
- Anomaly detection model (Phase 13).
- Auto-response (Phase 14).
- Cross-region replication (Phase 15).

**Ownership**
- **Drives:** Streaming-capable Backend Lead.
- **Reviews:** Security (webhook signatures, SSRF defenses), Frontend (live UI), Infra (Kafka).

---

## 3. Hard Dependencies & Sequencing

- Phase 5 producer SDK + audit topics.
- Phase 4 admin console BFF.
- Phase 0 Redis pub/sub for in-process broadcast.
- Phase 1 schema: `webhook_subscriptions`, `webhook_deliveries` tables.

Sequencing: live-feed-broadcaster → WebSocket fan-out → Live Activity UI → webhook subscriptions → webhook-fanout consumer → DLQ + replay tooling → saved-query scheduling.

---

## 4. Detailed Sub-Phases & Implementation Tasks

### 12.1 — Topic Design Lock-In

Continuing Phase 5:
- `audit.policy.{env}` — partitioned by `tenant_id`.
- `audit.access.{env}` — partitioned by `user_id` (tenant in payload).
- `audit.system.{env}` — partitioned by source service.

V1 single broker still; Phase 15 moves to 3-broker MSK / Confluent Cloud.

### 12.2 — Producer Reliability Final

Already established in Phase 5:
- `enable.idempotence=true`, `acks=all`, `compression=zstd`.
- Local disk buffer on Kafka unavailability.
- Alert at buffer ≥ 50% capacity.

### 12.3 — Consumers Layout

| Consumer                | Purpose                                            | New? |
| ----------------------- | -------------------------------------------------- | ---- |
| `clickhouse-sink`       | Phase 5                                            | no   |
| `worm-sink`             | Phase 5                                            | no   |
| `live-feed-broadcaster` | Push to admin UI via WebSocket                     | YES  |
| `webhook-fanout`        | POST events to customer webhooks (signed)          | YES  |
| `anomaly-detector`      | Phase 13 (stub here for wiring)                    | YES  |
| `saved-query-runner`    | Scheduler-driven; runs saved questions via Phase 10 graph | YES  |

### 12.4 — Live Activity Feed

Service `apps/live-feed-broadcaster` (Go):
- Consumes `audit.access.*` + `audit.policy.*`.
- Maintains WebSocket connections to admin console.
- Per-connection filter: `tenantId`, optional `userId`, `resource`, `decision`, `riskScoreMin`.
- Backpressure: server drops oldest events for slow consumers; emits `wsfeed_dropped_total`.
- Auth: WebSocket upgrade carries a short-lived JWT signed by api-gateway; per-connection tenant claim.

UI (Phase 4 page):
- Scrolling list with virtualized rows.
- Color-coded by decision and (Phase 13) risk score.
- Filter chips: user, resource, decision.
- Pause / Resume.
- Click → detail drawer with full event, trace ID, rewritten SQL hash, policy attribution.

### 12.5 — Webhook Subscriptions

Schema (new):
- `webhook_subscriptions(id, tenant_id, name, url, event_types text[], secret_ref Vault, is_active, created_at, updated_at, last_delivery_at, failure_count)`.
- `webhook_deliveries(id, subscription_id, event_id, attempt, status, status_code, response_body, duration_ms, attempted_at, next_retry_at)`.

Lifecycle:
- Admin creates subscription with URL + event types.
- Backend issues a `secret` (32 bytes) stored in Vault; returned once on creation.
- Consumer service `apps/webhook-fanout` reads matching events, POSTs with:
  - Body: canonical JSON of event.
  - Header `X-Janus-Signature: t=<ts>, v1=<hmac>`.
  - Header `X-Janus-Event-Type`, `X-Janus-Event-Id`, `X-Janus-Idempotency-Key`.
- Retries: exponential backoff (1m, 5m, 30m, 2h, 12h) up to 5 attempts within 24 h.
- Dead-letter: failed delivery moves to `webhook_dlq`; customer admin can replay manually.

### 12.6 — Webhook Security

- HMAC-SHA256 over `<timestamp>.<body>` with the subscription secret.
- Timestamp skew tolerance ≤ 5 min.
- URL allowlist:
  - HTTPS only.
  - DNS resolved at request time; refuse to connect to private/link-local IP ranges (anti-SSRF).
  - DNS pinning: refuse if DNS rebinding suspected.
- Per-tenant rate limit (e.g., 100 req/s) + global circuit breaker on customer endpoint 5xx (≥ 50% over 1 min → 5-min pause).
- Sensitive data redaction: per-subscription field allowlist (default sends all; customer can restrict).

### 12.7 — Customer-Facing Delivery UI

Admin console:
- Subscriptions page: list, create, edit, deactivate, regenerate secret.
- Per-subscription delivery log: last 1000 attempts, status, retry next.
- "Send test event" button.
- Per-subscription event filter editor (mini DSL for `event.decision = 'deny'` predicate).
- Webhook secret revealed exactly once on creation; rotation flow.

### 12.8 — Saved-Query Scheduling

A scheduler that:
- Reads `saved_questions.schedule_cron` (new column).
- Triggers a Phase 10 PEP graph run per schedule.
- Results delivered via:
  - Webhook to subscribed endpoints.
  - Email (Phase 16 enhancement).
  - Saved as a report in the admin UI.

Owners: same team as orchestrator; this phase wires the cron + consumer integration.

### 12.9 — Observability & Reliability

Metrics:
- `kafka_consumer_lag_seconds{topic, consumer}`.
- `wsfeed_active_connections{tenant}`.
- `wsfeed_dropped_total{tenant}`.
- `webhook_delivery_total{result}`, `webhook_delivery_duration`.
- `webhook_dlq_total{tenant}`.
- `saved_query_runs_total{tenant, status}`.

Alerts:
- Consumer lag > 60 s / 5 min.
- WebSocket drop rate > 5%.
- Webhook customer endpoint failing > 50% / 5 min (per-tenant).
- DLQ growth > 100/h.

### 12.10 — Tests

- **Load:** 50k events/s sustained; live feed handles 1k concurrent admin connections.
- **Webhook:** synthetic customer endpoint with 10% failure rate; verify retry behavior + DLQ.
- **Security:** SSRF probe (resolved private IP); verify refusal + audit.
- **Replay:** DLQ replay tool verified.

---

## 5. Architectural Gaps & Missing Requirements

1. **WebSocket scaling.** Stateful long-lived connections need session affinity / Redis pub/sub broadcast. Plan capacity.
2. **Tenant fairness on Kafka.** Noisy tenant could starve consumers; per-tenant rate-limit at producer recommended.
3. **Webhook payload schema versioning.** Customers will pin against the schema; lock and version (`X-Janus-Schema-Version`).
4. **Customer endpoint health.** Track each subscription's health score; deactivate after sustained failure (with notification).
5. **mTLS option for webhooks.** Some Enterprise tenants require mutual TLS; reserve a per-subscription cert option.
6. **Filter DSL constraints.** The per-subscription filter DSL must be bounded (like the policy DSL); design now.
7. **Saved-query cron security.** Cron expressions ≠ free-form; bound with a sanitizer.
8. **Real-time UI export.** Tenants will ask "download last 1k events" — keep an export hook that hits ClickHouse, not the live stream.
9. **GDPR for webhook payloads.** Tokenization applied; per-subscription allowlist of fields.
10. **Cost attribution for webhook traffic.** Track per-tenant outbound traffic.

---

## 6. Edge Cases & Failure Modes

| Scenario                                                          | Mitigation                                                                                       |
| ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| WebSocket consumer disconnects mid-stream                         | Resume from last `event_id` if requested; otherwise start from latest.                            |
| Slow admin browser                                                | Drop oldest; alert if > 5% dropped sustained.                                                    |
| Webhook secret leaked                                             | Rotation flow; old secret accepted for grace window with audit.                                  |
| Customer endpoint returns 200 but doesn't process                 | Out of platform's hands; document at-least-once delivery contract.                                |
| Customer endpoint times out                                       | 30 s budget; retry per backoff schedule.                                                         |
| SSRF attempt via DNS rebinding                                    | Resolve once, pin IP for the request; refuse private ranges.                                     |
| WebSocket flood (admin opens too many connections)                | Per-user connection cap (e.g., 5).                                                               |
| Saved query at scheduled time when user disabled / data source down | Skip run; emit audit event; alert subscriber after N consecutive misses.                       |
| Webhook headers oversized                                         | Cap at 16 KB; truncate with notice.                                                              |
| Customer endpoint requests data that exceeds payload size        | Cap event body at 64 KB; reference link for large payloads.                                       |
| Cross-tenant event leak via misconfigured WebSocket filter        | Filter validated server-side; tenant claim authoritative.                                        |
| Consumer crash mid-batch                                          | Idempotent on event_id; re-consume from last committed offset.                                    |

---

## 7. Non-Functional Concerns

### 7.1 Scalability
- WebSocket service scales with active connections; sticky sessions or Redis pub/sub fan-out.
- Webhook-fanout scales with subscriber × event rate.
- Saved-query scheduler shards by tenant; per-shard worker pool.

### 7.2 Security
- HMAC signing + skew tolerance.
- SSRF defenses (DNS pinning + private-range denylist).
- Webhook URLs registered: allowlist HTTPS + per-tenant cap on subscription count.
- Per-subscription field allowlist.
- Webhook secrets in Vault; rotation auditable.

### 7.3 Multi-Tenant Isolation
- Per-tenant Kafka consumer groups optional; partition keys protect ordering.
- Per-tenant webhook rate limits prevent noisy neighbor.
- Per-tenant WebSocket connection caps.

### 7.4 Concurrency
- Webhook consumer is parallelized per partition; per-subscription concurrency capped to preserve ordering for that subscription.
- WebSocket fan-out uses per-tenant goroutine; backpressure isolates slow clients.

### 7.5 Performance
- Event end-to-end (producer → WebSocket) p95 < 2 s.
- Webhook delivery start (event → first attempt) p95 < 5 s.
- Saved-query scheduler tick precision ≤ 30 s.

---

## 8. Recommended Improvements

### Architecture
- A **dispatcher pattern**: a single consumer reads events once and dispatches to a pluggable set of "sinks" (live-feed, webhook, anomaly). Reduces Kafka load.
- Use **NATS** or **Redis Streams** for the WebSocket fan-out layer (faster than Kafka for low-fanout local broadcast). Optional optimization.

### DX
- `webhook-cli send-test --subscription=…` for customer-side testing.
- A "Mock Customer Endpoint" container in dev compose for local webhook testing.
- Storybook for live-feed UI states.

### UX
- Subscription health badge.
- "Replay last 24 h" CTA for customers debugging.
- Webhook signature verification snippets for common languages.

### Reliability
- Health-aware customer endpoints: track per-endpoint failure rate; auto-deactivate with notice.
- Local disk buffer for webhook-fanout (events not yet attempted).
- Idempotency-key included in every delivery.

### Observability
- Per-subscription dashboards: delivery rate, p95 latency, failure %, DLQ.
- Live-feed dashboards: active connections per tenant, drop rate.
- End-to-end traces from producing service → WebSocket message.

### Maintainability
- Schema-versioned webhook payloads; backwards-compatible additions only.
- ADRs: WebSocket vs SSE; Kafka topic shape; webhook security model.
- Webhook contract published as OpenAPI for customer dev teams.

---

## 9. Technical Considerations

### 9.1 DB Design
- `webhook_subscriptions`, `webhook_deliveries`, `webhook_dlq` as above.
- `saved_questions.schedule_cron`, `last_run_at`, `next_run_at`.

### 9.2 API Contracts
- `POST /v1/webhooks` (create), `GET /v1/webhooks`, `DELETE`, `POST /v1/webhooks/{id}/rotate-secret`, `POST /v1/webhooks/{id}/test`.
- `GET /v1/webhooks/{id}/deliveries` for delivery log.
- `WS /v1/live-feed` with query params for filter.

### 9.3 RBAC
- `webhook.read`, `webhook.write`, `webhook.replay`, `live_feed.subscribe`.

### 9.4 Validation Flows
- URL validator (HTTPS, no private range).
- Cron sanitizer.
- Filter DSL bounded similar to policy DSL.

### 9.5 Caching
- WebSocket auth-token cache.
- Webhook subscription cache 30 s.

### 9.6 Queues & Background Jobs
- Webhook retry queue (Redis-backed).
- DLQ replay worker.
- Saved-query scheduler.

### 9.7 Audit Logs
- Every webhook delivery + failure audited.
- Every subscription mutation audited.
- WebSocket subscribe / unsubscribe events.

### 9.8 Retry & Idempotency
- Webhook idempotency-key on every delivery.
- Saved-query scheduler dedupes by `(saved_question_id, scheduled_at)`.

### 9.9 Monitoring
Alerts: lag, dropped, DLQ growth, failure rate per subscription.

### 9.10 CI/CD
- Webhook contract tests against mock customer endpoint.
- WebSocket load test in `staging`.
- DLQ replay rehearsed monthly.

---

## 10. Risks, Rollback & Future Extensibility

### Risks
| Risk                                                                  | Likelihood | Impact   | Mitigation                                                                                       |
| --------------------------------------------------------------------- | ---------- | -------- | ------------------------------------------------------------------------------------------------ |
| Webhook abuse against customer endpoints                              | Med        | High     | Rate limit + circuit breaker per subscription.                                                  |
| SSRF via webhook URL                                                  | Med        | Critical | DNS pinning + private-range denylist.                                                            |
| WebSocket flood (DoS)                                                 | Med        | Med      | Per-user connection cap + auth.                                                                   |
| Kafka cluster outage                                                  | Low        | High     | Producer disk buffer + alarm; live-feed shows banner.                                            |
| Saved-query schedule storms (1000 tenants at 09:00)                   | Med        | High     | Jitter + sharded scheduler.                                                                       |
| Webhook payload drift (customers pin schema)                          | High       | Med      | Schema versioning + deprecation policy.                                                          |
| Customer endpoint accepts payload but logs it insecurely              | Out of scope | Med    | Document; recommend HMAC verification.                                                            |

### Rollback
- Per-feature flags: live-feed, webhooks, scheduled saved queries.
- Per-subscription deactivation.
- Consumer group offset reset tooling.

### Future Extensibility
- mTLS webhooks.
- Webhook payload encryption per subscription public key.
- Push to Slack/PagerDuty natively (Phase 13/14 leverage).
- Real-time risk-score live overlay (Phase 13).
- Customer-side SDKs for webhook verification.

---

## 11. Deliverables & Acceptance Criteria

### Deliverables
- [ ] `live-feed-broadcaster` deployed.
- [ ] Admin Live Activity page live.
- [ ] `webhook-fanout` deployed with retries + DLQ.
- [ ] Admin UI for subscriptions + delivery log.
- [ ] Saved-query scheduler.
- [ ] Webhook contract OpenAPI published.

### Acceptance Criteria
- [ ] Event visible in Live Activity < 2 s after query.
- [ ] Webhook delivered within 5 s p95.
- [ ] HMAC signature verifiable; SSRF refused.
- [ ] Kafka outage (1 h) doesn't drop events thanks to producer buffer.
- [ ] DLQ replay tool exercised + verified.

---

## 12. Production Readiness Checklist

- [ ] Per-tenant rate limits.
- [ ] SSRF defenses red-teamed.
- [ ] Customer-facing docs + SDK snippets.
- [ ] On-call runbooks: Kafka outage, webhook flood, DLQ growth.
- [ ] Capacity model for WebSocket fan-out.
- [ ] Webhook security audit passed.

---

## 13. Remaining Risks Carried Forward

- **Anomaly detector** (Phase 13) is the next consumer of the stream.
- **Auto-response** (Phase 14) layers on webhooks + risk events.
- **Multi-region Kafka** in Phase 15.
- **mTLS for webhooks** deferred to Phase 14+.
- **Email delivery** for saved queries deferred to Phase 16.
- **Cost attribution** for webhook traffic tracked but not yet billed.
