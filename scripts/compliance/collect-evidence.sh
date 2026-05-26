#!/usr/bin/env bash
# Trigger compliance evidence collection for all tenants.
# Run weekly from CI or cron; records go into compliance_evidence table.
# Usage: ./collect-evidence.sh [--tenant-id <id>] [--dry-run]

set -euo pipefail

API_URL="${GOVERNANCE_API_URL:-http://localhost:8080}"
TOKEN="${GOVERNANCE_SERVICE_TOKEN:-}"
DRY_RUN=false
SPECIFIC_TENANT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tenant-id) SPECIFIC_TENANT="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true; shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

AUTH_ARGS=()
if [[ -n "$TOKEN" ]]; then
  AUTH_ARGS=("-H" "Authorization: Bearer $TOKEN")
fi

if [[ -n "$SPECIFIC_TENANT" ]]; then
  TENANT_IDS=("$SPECIFIC_TENANT")
else
  TENANT_IDS=($(curl -sf "${AUTH_ARGS[@]}" "$API_URL/v1/admin/tenants" | jq -r '.tenants[].id' || echo ""))
fi

echo "[collect-evidence] Collecting evidence for ${#TENANT_IDS[@]} tenant(s)"

for tid in "${TENANT_IDS[@]}"; do
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "  [DRY RUN] Would collect evidence for tenant $tid"
    continue
  fi

  HTTP_CODE=$(curl -sf -o /dev/null -w "%{http_code}" -X POST \
    "${AUTH_ARGS[@]}" \
    "$API_URL/v1/admin/$tid/compliance/evidence/collect" 2>/dev/null || echo "000")

  if [[ "$HTTP_CODE" == "202" ]]; then
    echo "  [OK] tenant=$tid evidence collection triggered"
  else
    echo "  [WARN] tenant=$tid http=$HTTP_CODE (non-fatal)"
  fi
done

echo "[collect-evidence] Done."
