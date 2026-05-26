# DR Runbook: PostgreSQL Primary Region Loss

**Scenario:** The Aurora writer cluster in `us-east-1` is unreachable (full region outage or isolated partition).  
**RTO target:** < 30 minutes  
**RPO target:** < 5 minutes  
**Owner:** SRE Lead  
**Last drilled:** _fill in after drill_

---

## Pre-requisites

- AWS CLI configured with `governance-sre` role.
- `kubectl` contexts: `governance-prod-us-east-1` and `governance-prod-eu-west-1`.
- Access to Vault for secret rotation post-failover.
- PagerDuty incident already opened.

---

## Step 1 — Confirm the Outage (≤ 3 min)

```bash
# Check Aurora global cluster status
aws rds describe-global-clusters \
  --global-cluster-identifier governance-prod-global \
  --region us-east-1 \
  --query 'GlobalClusters[0].GlobalClusterMembers'

# Check replication lag before deciding to promote
aws rds describe-db-cluster-parameters \
  --db-cluster-parameter-group-name governance-prod-aurora-pg16 \
  --region eu-west-1

# Check last replication lag from Grafana:
# Dashboard: "Replication Lag" → panel "Aurora Replication Lag (seconds)"
# If lag_seconds > 300 (5 min), halt and escalate — RPO may be breached.
```

**Decision gate:** If `lag_seconds > 300`, call VP Engineering before proceeding. Do NOT promote if RPO will be breached.

---

## Step 2 — Detach Secondary from Global Cluster (≤ 5 min)

```bash
# This makes eu-west-1 Aurora an independent writable cluster.
# WARNING: Once detached, us-east-1 data after the last sync point is lost.

aws rds remove-from-global-cluster \
  --global-cluster-identifier governance-prod-global \
  --db-cluster-identifier governance-prod-eu-aurora-secondary \
  --region eu-west-1

# Wait for status = "available"
aws rds wait db-cluster-available \
  --db-cluster-identifier governance-prod-eu-aurora-secondary \
  --region eu-west-1
```

---

## Step 3 — Update DNS Write Record (≤ 2 min)

```bash
# Update write.api.governance-platform.io → eu-west-1 ALB
# Edit infra/terraform/environments/prod-us-east-1/main.tf:
#   primary_region = "eu-west-1"
# Then apply:

cd infra/terraform/modules/route53-multiregion
terraform apply -var="primary_region=eu-west-1" -auto-approve

# Verify DNS propagation (TTL is 30 s):
dig write.api.governance-platform.io +short
```

---

## Step 4 — Update api-gateway Home-Region Routing (≤ 2 min)

```bash
# Patch the api-gateway ConfigMap to set WRITE_REGION=eu-west-1
kubectl --context governance-prod-eu-west-1 \
  set env deployment/api-gateway WRITE_REGION=eu-west-1

# Rolling restart (HPA ensures no downtime):
kubectl --context governance-prod-eu-west-1 rollout restart deployment/api-gateway
kubectl --context governance-prod-eu-west-1 rollout status deployment/api-gateway
```

---

## Step 5 — Verify Write Traffic (≤ 3 min)

```bash
# Synthetic probe: create a test policy and verify it persists
scripts/synthetic-probes/probe-write.sh --region eu-west-1

# Check Grafana:
# Dashboard: "API Gateway" → "5xx Rate" — must remain < 1%
# Dashboard: "PDP" → "Decision Latency p99" — must remain < 50 ms
```

---

## Step 6 — Rotate Secrets (≤ 5 min)

```bash
# The eu-west-1 cluster has a different endpoint; update Vault secrets.
vault kv put governance-platform/prod/postgres \
  host="$(aws rds describe-db-clusters \
    --db-cluster-identifier governance-prod-eu-aurora-secondary \
    --region eu-west-1 \
    --query 'DBClusters[0].Endpoint' --output text)" \
  ...

# Trigger external-secrets-operator refresh:
kubectl --context governance-prod-eu-west-1 \
  annotate externalsecret postgres-credentials \
  force-sync="$(date +%s)"
```

---

## Step 7 — Notify Stakeholders (≤ 2 min)

- Update status page: https://status.governance-platform.io
- Post to #incidents Slack channel.
- Update PagerDuty incident with RTO/RPO achieved.

---

## Step 8 — Record Drill Results

```sql
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES ('pg_failover', 'staging', '<your-name>', '<start>', '<end>',
  <actual_rto>, <actual_rpo>, <rto_met>, <rpo_met>, '<observations>');
```

---

## Recovery (Re-promote us-east-1 after outage resolves)

1. Create a new global cluster using eu-west-1 as primary.
2. Add us-east-1 as a secondary via `aws rds create-global-cluster`.
3. Let replication catch up; verify lag < 60 s.
4. Optionally fail back to us-east-1 (repeat this runbook in reverse).
5. Update DNS write record back to us-east-1.

---

## Contacts

| Role | Name | PagerDuty |
|------|------|-----------|
| SRE On-call | (rotation) | @sre-oncall |
| Platform Lead | (rotation) | @platform-lead |
| VP Engineering | (direct) | @vpe |
