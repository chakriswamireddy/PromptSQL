# DR Runbook: Kafka Cluster Loss

**Scenario:** MSK cluster in the primary region is unavailable (all brokers down, network partition, or corruption).  
**RTO target:** < 30 minutes  
**RPO target:** < 5 minutes (audit events buffered on disk via pkg/audit spool)  
**Owner:** SRE Lead

---

## Step 1 — Confirm Cluster Status (≤ 3 min)

```bash
# Check MSK cluster state
aws kafka describe-cluster \
  --cluster-arn "$(terraform -chdir=infra/terraform/environments/prod-us-east-1 output -raw kafka_cluster_arn)" \
  --region us-east-1 \
  --query 'ClusterInfo.State'

# Check consumer lag (Prometheus):
# Metric: kafka_consumer_group_lag{cluster="governance-prod-us-east-1"}
# If lag is < 1000 messages, the disk spool buffer can cover the gap.
```

---

## Step 2 — Switch Producers to eu-west-1 Kafka (≤ 5 min)

The audit SDK ([pkg/audit](../../pkg/audit)) uses a disk spool as fallback. When the primary Kafka is unreachable, producers automatically spool to local disk. To drain the spool to the secondary broker:

```bash
# Update KAFKA_BROKERS env var for all producer services in eu-west-1
for SVC in api-gateway anomaly-detector auto-responder audit-clickhouse-sink; do
  kubectl --context governance-prod-eu-west-1 \
    set env deployment/$SVC KAFKA_BROKERS="${EU_KAFKA_BOOTSTRAP}"
  kubectl --context governance-prod-eu-west-1 rollout restart deployment/$SVC
done

# Drain disk spool (service reads from /var/governance/audit-spool):
kubectl --context governance-prod-eu-west-1 \
  exec -it deployment/audit-clickhouse-sink -- /bin/sh -c \
  "DRAIN_SPOOL=true /app/sink"
```

---

## Step 3 — Verify Consumers on Secondary (≤ 5 min)

```bash
# Confirm audit topics exist on eu-west-1 (replicated by MirrorMaker 2)
kafka-topics.sh \
  --bootstrap-server "${EU_KAFKA_BOOTSTRAP}" \
  --list | grep audit

# Restart ClickHouse sink against eu-west-1 broker
kubectl --context governance-prod-eu-west-1 \
  set env deployment/audit-clickhouse-sink KAFKA_BROKERS="${EU_KAFKA_BOOTSTRAP}"
kubectl --context governance-prod-eu-west-1 rollout restart deployment/audit-clickhouse-sink
```

---

## Step 4 — Verify Audit Pipeline Health (≤ 5 min)

```bash
# Grafana: Dashboard "Audit Pipeline" → "Events Ingested per Second"
# Should recover to pre-failure baseline within 5 min of drain completion.

# Verify WORM sink is still draining (it has its own retry loop):
kubectl --context governance-prod-eu-west-1 logs -l app=audit-worm-sink --tail=50
```

---

## Step 5 — Notify + Record

- Update PagerDuty incident with RTO/RPO.
- Post to #incidents.
- Record drill results:

```sql
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES ('kafka_failover', 'staging', '<your-name>', '<start>', '<end>',
  <actual_rto>, <actual_rpo>, <rto_met>, <rpo_met>, '<observations>');
```

---

## Recovery

1. Rebuild MSK cluster in us-east-1 (Terraform apply).
2. Restore topic configuration from `scripts/kafka-topics-setup.sh`.
3. Configure MirrorMaker 2 to back-replicate eu→us until caught up.
4. Switch producers back to us-east-1; wait for consumer lag = 0.
5. Disable MirrorMaker 2 back-replication.
