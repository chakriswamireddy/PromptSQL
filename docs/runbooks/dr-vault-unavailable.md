# DR Runbook: Vault Unseal / Unavailability

**Scenario:** HashiCorp Vault (HCP Vault or self-managed HA cluster) is unsealed but unreachable, or the KMS auto-unseal key is unavailable.  
**RTO target:** < 30 minutes  
**RPO target:** N/A (Vault is config/secret storage, not data storage)  
**Owner:** Security Engineer + SRE Lead

---

## Impact Assessment

All services that read secrets from Vault via external-secrets-operator will:
- Continue using **already-cached secrets** in Kubernetes Secrets (populated on last successful sync).
- Fail to **rotate** secrets until Vault recovers.
- Fail to **issue new JWT signing keys** or **unseal new Vault replicas**.

Short outages (< 1 hour) are typically transparent to running pods.

---

## Step 1 — Diagnose (≤ 5 min)

```bash
# Check HCP Vault status (if using HCP):
# https://status.hashicorp.com

# Check self-managed Vault pods:
kubectl --context governance-prod-us-east-1 \
  get pods -n vault -l app.kubernetes.io/name=vault

# Check seal status on each pod:
kubectl --context governance-prod-us-east-1 \
  exec -n vault vault-0 -- vault status

# Check KMS auto-unseal key:
aws kms describe-key --key-id alias/governance-prod-vault-autounseal \
  --region us-east-1 --query 'KeyMetadata.{State:KeyState,Enabled:Enabled}'
```

---

## Step 2a — If Vault Is Sealed (KMS Auto-Unseal)

```bash
# The Vault pods should unseal automatically if the KMS key is accessible.
# If not, check the IAM role attached to Vault's service account:

kubectl --context governance-prod-us-east-1 \
  get serviceaccount vault -n vault -o yaml | grep eks.amazonaws.com

# Validate the IRSA role can call KMS:
aws sts assume-role-with-web-identity \
  --role-arn arn:aws:iam::ACCOUNT:role/governance-prod-vault-kms \
  --web-identity-token "$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
  --role-session-name vault-test

# If KMS key is disabled (rare), re-enable:
aws kms enable-key --key-id alias/governance-prod-vault-autounseal --region us-east-1
```

---

## Step 2b — If KMS Key Is Permanently Unavailable (Break-Glass)

> **IMPORTANT:** This procedure uses the emergency unseal keys stored in the physical safe at the company's registered address. Two-person integrity required (Security Engineer + VP Engineering).

1. Retrieve the 5-shard Shamir unseal keys from the physical safe.
2. Unseal each Vault pod manually:

```bash
for POD in vault-0 vault-1 vault-2; do
  for SHARD in "$SHARD_1" "$SHARD_2" "$SHARD_3"; do
    kubectl --context governance-prod-us-east-1 \
      exec -n vault $POD -- vault operator unseal "$SHARD"
  done
done
```

---

## Step 3 — Verify Secret Sync (≤ 5 min)

```bash
# Check external-secrets-operator is syncing:
kubectl --context governance-prod-us-east-1 \
  get externalsecrets -n governance-platform

# Force sync of critical secrets:
kubectl --context governance-prod-us-east-1 \
  annotate externalsecret postgres-credentials \
  force-sync="$(date +%s)" -n governance-platform

kubectl --context governance-prod-us-east-1 \
  annotate externalsecret redis-credentials \
  force-sync="$(date +%s)" -n governance-platform
```

---

## Step 4 — Rotate Vault Root Token (If Compromised)

If the Vault root token may be exposed:

```bash
# Generate a new root token with operator generate-root:
kubectl --context governance-prod-us-east-1 \
  exec -n vault vault-0 -- vault operator generate-root -init

# Follow the generate-root ceremony with multiple key shards.
# Full procedure: https://developer.hashicorp.com/vault/tutorials/operations/generate-root
```

---

## Step 5 — Verify All Services Healthy

```bash
# Check that PDP, api-gateway, proxy can still authenticate after Vault recovery:
scripts/synthetic-probes/probe-pdp.sh --region us-east-1
scripts/synthetic-probes/probe-proxy.sh --region us-east-1
```

---

## Step 6 — Record

```sql
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES ('vault_unavailable', 'staging', '<your-name>', '<start>', '<end>',
  <actual_rto>, NULL, <rto_met>, NULL, '<observations>');
```
