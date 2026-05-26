# DR Runbook: WORM Bucket Compromise

**Scenario:** S3 Object Lock WORM audit bucket is suspected of tampering, unauthorized access, or accidental deletion of objects.  
**RTO target:** N/A (bucket is read-only cold storage; no ingest interruption)  
**RPO target:** N/A (cross-region CRR replicates within 15 min SLA)  
**Owner:** Security Engineer + SRE Lead

---

## IMPORTANT: This Is a Security Incident

Before following this runbook, **open a security incident** in PagerDuty with severity P1 and notify the Security Lead immediately.

---

## Step 1 — Isolate the Bucket (≤ 5 min)

```bash
# Revoke all IAM roles that have write access except the audit-worm-sink service account.
# Never delete Object Lock-protected objects — they cannot be deleted before retention expires.

# Check who accessed the bucket in the last 24 hours (CloudTrail):
aws cloudtrail lookup-events \
  --lookup-attributes AttributeKey=ResourceName,AttributeValue=governance-platform-audit-worm-prod-us-east-1 \
  --start-time "$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)" \
  --region us-east-1 \
  --query 'Events[*].{Time:EventTime,User:Username,Action:EventName}'

# Block all public access (already set but verify):
aws s3api get-public-access-block \
  --bucket governance-platform-audit-worm-prod-us-east-1
```

---

## Step 2 — Verify Cross-Region Replica Integrity (≤ 10 min)

```bash
# Compare object counts between primary and replica:
aws s3 ls s3://governance-platform-audit-worm-prod-us-east-1 --recursive --summarize \
  | grep "Total Objects"
aws s3 ls s3://governance-platform-audit-worm-prod-eu-west-1 --recursive --summarize \
  | grep "Total Objects"

# If counts differ by more than 15 min of ingest, CRR may be lagging — not necessarily compromise.
# Check CRR metrics in CloudWatch:
# Metric: ReplicationLatency{DestinationBucket=..., MetricsId=replicate-to-eu}
```

---

## Step 3 — Verify Hash-Chain Integrity (≤ 10 min)

The audit hash-chain verifier runs hourly against PostgreSQL. For immediate verification:

```bash
kubectl --context governance-prod-us-east-1 \
  create job --from=cronjob/audit-chain-verifier emergency-verify-$(date +%s)

# Watch logs for any "CHAIN_BREAK" entries:
kubectl --context governance-prod-us-east-1 \
  logs -f job/emergency-verify-...
```

A CHAIN_BREAK alert means tamper evidence was found. Preserve all logs and escalate to forensics.

---

## Step 4 — Scope Damage

Determine:
- Which time range is affected?
- Were any objects overwritten? (Object Lock COMPLIANCE mode prevents this — if objects were deleted/overwritten, the retention policy was violated and AWS must investigate.)
- Is the KMS key still valid? Check:

```bash
aws kms describe-key \
  --key-id alias/governance-prod-audit-worm \
  --region us-east-1
```

---

## Step 5 — Notify Compliance + Legal

Per SOC 2 requirements, notify:
- Compliance Officer within 1 hour of confirmed breach.
- Legal within 4 hours.
- Affected tenants per their data processing agreement within 72 hours.

---

## Step 6 — Preserve Evidence

```bash
# Enable CloudTrail + S3 server access logging (may already be on):
aws s3api put-bucket-logging \
  --bucket governance-platform-audit-worm-prod-us-east-1 \
  --bucket-logging-status file://infra/s3/logging-config.json

# Export CloudTrail logs for the incident window:
aws cloudtrail get-event-selectors \
  --trail-name governance-prod-trail \
  --region us-east-1
```

---

## Step 7 — Record

```sql
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES ('worm_compromise', 'staging', '<your-name>', '<start>', '<end>',
  NULL, NULL, NULL, NULL, '<observations>');
```
