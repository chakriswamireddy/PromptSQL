# DR Runbook: ClickHouse Loss

**Scenario:** ClickHouse Cloud cluster (or self-managed Altinity cluster) is unavailable or corrupted.  
**RTO target:** < 30 minutes (audit queries degrade; audit ingest continues to Kafka)  
**RPO target:** Ingest RPO = 0 (Kafka buffers events). Query RPO = time since last snapshot.  
**Owner:** SRE Lead + ML Engineer

---

## Impact Assessment

ClickHouse is the **hot audit query store**. Loss affects:
- Admin console "Audit" page (reads from ClickHouse).
- UEBA anomaly detector baselines (reads `audit_access` materialized views).
- Compliance reporting queries.

Audit **ingest is not affected** (events remain in Kafka until ClickHouse recovers).

---

## Step 1 — Confirm ClickHouse Unavailability (≤ 2 min)

```bash
# ClickHouse Cloud status: https://status.clickhouse.cloud
# Self-managed: check pod health
kubectl --context governance-prod-us-east-1 \
  get pods -l app=clickhouse -n governance-platform

# Prometheus metric:
# clickhouse_query_duration_seconds_count{cluster="governance-prod"} — should show activity
```

---

## Step 2 — Halt Admin Console Audit Reads (≤ 2 min)

Prevent cascading errors by returning a maintenance page for audit queries:

```bash
kubectl --context governance-prod-us-east-1 \
  set env deployment/admin-console AUDIT_READONLY_MODE=true
kubectl --context governance-prod-us-east-1 rollout restart deployment/admin-console
```

---

## Step 3 — Let Kafka Buffer (No Action Needed ≤ 7 days)

The audit ClickHouse sink will retry indefinitely (exponential backoff, max 5 min between retries). Kafka's `log.retention.hours=168` (7 days) means events are safe for one week.

Monitor Kafka lag:
```bash
# Prometheus metric:
# kafka_consumer_group_lag{group="audit-clickhouse-sink"} — will grow; should stay < 7 days of events
```

---

## Step 4 — Restore ClickHouse (ClickHouse Cloud)

For ClickHouse Cloud, contact support via https://clickhouse.cloud/support.

For self-managed Altinity:
```bash
# Restore from the latest snapshot (stored in S3)
kubectl --context governance-prod-us-east-1 apply -f infra/clickhouse/restore-job.yaml

# Verify schema:
clickhouse-client --query "SHOW TABLES" --database governance
# Expected: audit_policy, audit_access, audit_system, + 3 materialized views
```

---

## Step 5 — Drain Kafka Backlog (≤ 20 min per day of backlog)

```bash
# Re-enable the sink with increased parallelism to drain backlog:
kubectl --context governance-prod-us-east-1 \
  set env deployment/audit-clickhouse-sink BATCH_SIZE=5000 CONCURRENCY=8
kubectl --context governance-prod-us-east-1 rollout restart deployment/audit-clickhouse-sink

# Monitor drain progress:
# Grafana: "Audit Pipeline" → "Kafka Consumer Lag"
```

---

## Step 6 — Re-enable Audit Reads

```bash
kubectl --context governance-prod-us-east-1 \
  set env deployment/admin-console AUDIT_READONLY_MODE=false
kubectl --context governance-prod-us-east-1 rollout restart deployment/admin-console
```

---

## Step 7 — Record

```sql
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES ('clickhouse_loss', 'staging', '<your-name>', '<start>', '<end>',
  <actual_rto>, <actual_rpo>, <rto_met>, <rpo_met>, '<observations>');
```
