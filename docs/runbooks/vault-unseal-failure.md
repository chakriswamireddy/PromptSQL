# Runbook: Vault Unseal Failure

**Severity:** Critical  
**Owner:** Platform / Security Team  
**Last reviewed:** 2026-05-21

## Symptoms

- `vault status` returns `Sealed: true`
- Services fail to start / log `permission denied` fetching secrets
- Vault health check endpoint returns HTTP 503

## Local dev (Docker Compose dev mode)

Dev-mode Vault starts pre-unsealed. If it reports sealed after restart:

```bash
docker compose -f infra/docker-compose.yml restart vault
# Wait ~10 s then:
vault status
```

Dev mode auto-unseals on startup. If it stays sealed, the container is misconfigured — `make nuke && make up`.

## Cloud / production (auto-unseal via KMS)

Vault is configured for auto-unseal using the per-environment KMS key (`infra/terraform/modules/kms/main.tf`).

1. Check KMS key availability in the cloud console — ensure the key is not disabled or pending deletion.
2. Check IAM permissions — the Vault service account must have `kms:Decrypt` on the CMK.
3. Check Vault logs: `kubectl logs -n vault vault-0` (after Phase 15).
4. If auto-unseal is broken due to KMS outage: follow manual Shamir unseal procedure using break-glass key shares stored in the security team's vault (physical).

## Break-glass manual unseal (emergency only)

Requires **two** key-holders present (M-of-N Shamir threshold).

```bash
vault operator unseal <share-1>
vault operator unseal <share-2>
```

Every manual unseal must be followed by a post-mortem and rotation of key shares.

## Prevention

- Vault auto-unseal KMS key has a backup key in a secondary region.
- Unseal failure alert fires within 2 min (see `infra/alerts/platform-phase0.yml`).
- DR drill must verify auto-unseal recovery — required before Phase 15 GA gate.
