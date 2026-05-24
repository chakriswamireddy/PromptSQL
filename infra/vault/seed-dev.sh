#!/usr/bin/env bash
# Seed Vault dev-mode instance with per-service AppRoles and example secrets.
# Run once after "make up" with VAULT_ADDR and VAULT_TOKEN in env.
set -euo pipefail

VAULT_ADDR="${VAULT_ADDR:-http://127.0.0.1:8200}"
VAULT_TOKEN="${VAULT_TOKEN:-root}"
export VAULT_ADDR VAULT_TOKEN

echo "==> Enabling secrets engine"
vault secrets enable -path=secret kv-v2 2>/dev/null || true

echo "==> Enabling AppRole auth"
vault auth enable approle 2>/dev/null || true

for SERVICE in api-gateway pdp proxy schema-crawler ai-orchestrator; do
  echo "==> Configuring AppRole for $SERVICE"
  vault policy write "$SERVICE" "infra/vault/policies/${SERVICE}.hcl"

  vault write "auth/approle/role/$SERVICE" \
    token_policies="$SERVICE" \
    token_ttl=24h \
    token_max_ttl=24h

  echo "==> Writing example secrets for $SERVICE"
  vault kv put "secret/governance/$SERVICE/database" \
    password="changeme-${SERVICE}" \
    username="${SERVICE}_user"
done

echo "==> Vault seed complete"
