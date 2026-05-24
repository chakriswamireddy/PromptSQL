# Vault policy for the api-gateway service AppRole.
# TTL is set at the AppRole level to <= 24h.

path "secret/data/governance/api-gateway/*" {
  capabilities = ["read"]
}

path "secret/metadata/governance/api-gateway/*" {
  capabilities = ["list"]
}
