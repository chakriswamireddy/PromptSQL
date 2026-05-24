# Observability Guide

> Authoritative registry for OTel resource attributes, span attributes, metric names, and log schema. New attributes must be added here before use. Naming drift is prevented by lint (`golangci-lint` custom rule for span names in Go; ESLint for TypeScript).

---

## Resource Attributes (service-level, set at init)

| Attribute | Source | Example |
|---|---|---|
| `service.name` | `Config.ServiceName` | `api-gateway` |
| `service.version` | `SERVICE_VERSION` env | `1.2.3` |
| `deployment.environment` | `DEPLOYMENT_ENVIRONMENT` env | `staging` |
| `tenant.id` | Injected per-request (empty string for non-tenant spans) | `00000000-…` |

---

## Span Naming Convention

```
<service>.<verb>.<resource>
```

Examples:
- `pdp.evaluate.policy`
- `proxy.rewrite.query`
- `audit-pipeline.produce.event`
- `schema-crawler.crawl.table`
- `api-gateway.forward.request`

---

## Span Attributes (request-level)

### Identity & Tenancy

| Key | Type | Example | Required |
|---|---|---|---|
| `tenant.id` | string | `<uuid>` | Yes on all tenant-scoped spans |
| `user.id` | string | `<uuid>` | Yes on all authenticated spans |
| `session.id` | string | `<uuid>` | Yes when SessionContext is present |
| `actor.role` | string | `data_analyst` | Resolved server-side; never from raw JWT |

### Policy & Authorization

| Key | Type | Example | Required |
|---|---|---|---|
| `policy.decision` | string | `ALLOW` / `DENY` | Yes on every PDP evaluation span |
| `policy.version` | string | `<uuid>` | Yes — the evaluated policy set version |
| `policy.rule_count` | int | `12` | Number of rules evaluated |
| `resource.type` | string | `table` / `column` / `row` | The governed resource type |
| `resource.id` | string | `public.users.email` | Fully-qualified resource identifier |

### HTTP

| Key | Type | Notes |
|---|---|---|
| `http.method` | string | OTel semantic conventions |
| `http.route` | string | Template form (`/v1/policies/{id}`), not the actual URL |
| `http.status_code` | int | |
| `http.request_id` | string | Propagated from `X-Request-ID` header |

### gRPC

| Key | Type | Notes |
|---|---|---|
| `rpc.service` | string | e.g. `governance.pdp.v1.PolicyService` |
| `rpc.method` | string | e.g. `Evaluate` |
| `rpc.grpc.status_code` | int | gRPC status code integer |

### Database

| Key | Type | Notes |
|---|---|---|
| `db.system` | string | `postgresql` |
| `db.name` | string | `governance` |
| `db.operation` | string | `SELECT` / `INSERT` / etc. |
| `db.sql.table` | string | For single-table queries only |
| `db.rls.role` | string | The role set via `SET LOCAL ROLE` for each query |

### Messaging (Kafka)

| Key | Type | Notes |
|---|---|---|
| `messaging.system` | string | `kafka` |
| `messaging.destination` | string | Full topic name including env prefix |
| `messaging.kafka.consumer.group` | string | Consumer group name |
| `messaging.idempotency_key` | string | UUID attached to every produce span |

### Audit

| Key | Type | Notes |
|---|---|---|
| `audit.action` | string | Dot-notation `<domain>.<verb>`, e.g. `policy.evaluate` |
| `audit.outcome` | string | `allowed` / `denied` / `error` |
| `audit.hash_chain` | string | Previous hash; present on audit record spans |

---

## Metric Names

All metrics use snake_case. Cross-service standard metrics:

| Metric | Type | Labels | SLO Use |
|---|---|---|---|
| `request_duration_seconds` | histogram | `service`, `method`, `status` | Latency SLO |
| `request_in_flight` | gauge | `service` | Capacity planning |
| `errors_total` | counter | `service`, `error_code` | Error rate SLO |
| `cache_hit_ratio` | gauge | `service`, `cache` | Cache efficiency |
| `policy_decisions_total` | counter | `tenant_id`, `decision` | Business metric |
| `kafka_produce_total` | counter | `topic`, `outcome` | Pipeline throughput |
| `kafka_lag` | gauge | `consumer_group`, `topic` | Pipeline health |

---

## Sampling Configuration

| Environment | Head sampler | Notes |
|---|---|---|
| `local` | `AlwaysSample` | Full traces for debugging |
| `dev` | `TraceIDRatioBased(1.0)` | Full traces |
| `staging` | `TraceIDRatioBased(0.1)` | 10% sample |
| `prod` | `TraceIDRatioBased(0.01)` | 1% sample; tail sampling in collector |

Set via `OTEL_SAMPLING_RATE` env var (see `pkg/telemetry`).

---

## Log Schema

Every log line from `pkg/logging` / `packages/telemetry` is JSON:

```json
{
  "level": "info",
  "ts": "2026-05-21T12:00:00Z",
  "service": "pdp",
  "version": "1.0.0",
  "env": "prod",
  "trace_id": "<w3c-trace-id>",
  "span_id": "<span-id>",
  "tenant_id": "<uuid>",
  "msg": "policy evaluation complete"
}
```

`trace_id` and `span_id` are automatically injected from the active OTel context.

---

## Trace Exemplars

Prometheus histograms MUST have exemplars enabled so Grafana dashboards can link from a latency spike directly to a representative Jaeger trace. The `pkg/telemetry` package enables this automatically.

---

## Alert Runbooks

All alert rules reference a `runbook:` annotation pointing to `docs/runbooks/<service>.md`. Every alert **must** have a corresponding runbook before the phase is marked complete.

---

## Dashboard Inventory

| Dashboard | Path | Phase |
|---|---|---|
| Platform Overview | `infra/grafana/dashboards/platform-overview.json` | Phase 0 |
| PDP Policy Evaluations | `infra/grafana/dashboards/pdp.json` | Phase 3 |
| Audit Pipeline | `infra/grafana/dashboards/audit.json` | Phase 5 |
| UEBA Risk Scores | `infra/grafana/dashboards/ueba.json` | Phase 13 |
