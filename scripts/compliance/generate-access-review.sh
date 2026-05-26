#!/usr/bin/env bash
# Generate quarterly access reviews for all tenants.
# Usage: ./generate-access-review.sh [--tenant-id <id>] [--dry-run]
#
# Requires: curl, jq, GOVERNANCE_API_URL, GOVERNANCE_SERVICE_TOKEN env vars.

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

auth_header() {
  if [[ -n "$TOKEN" ]]; then
    echo "-H" "Authorization: Bearer $TOKEN"
  else
    echo ""
  fi
}

# Fetch tenant list from the platform.
if [[ -n "$SPECIFIC_TENANT" ]]; then
  TENANT_IDS=("$SPECIFIC_TENANT")
else
  echo "[generate-access-review] Fetching tenant list..."
  TENANT_IDS=($(curl -sf $(auth_header) "$API_URL/v1/admin/tenants" | jq -r '.tenants[].id' || echo ""))
fi

if [[ ${#TENANT_IDS[@]} -eq 0 ]]; then
  echo "[generate-access-review] No tenants found or API unavailable. Exiting."
  exit 0
fi

QUARTER=$(date +"%Y-Q$(( ($(date +%-m) - 1) / 3 + 1 ))")
echo "[generate-access-review] Generating $QUARTER reviews for ${#TENANT_IDS[@]} tenant(s)"

SUCCESS=0
FAILED=0

for tid in "${TENANT_IDS[@]}"; do
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "  [DRY RUN] Would generate access review for tenant $tid"
    continue
  fi

  RESPONSE=$(curl -sf -w "\n%{http_code}" -X POST \
    $(auth_header) \
    -H "Content-Type: application/json" \
    "$API_URL/v1/admin/$tid/access-reviews/generate" 2>&1 || true)

  HTTP_CODE=$(echo "$RESPONSE" | tail -1)
  BODY=$(echo "$RESPONSE" | head -1)

  if [[ "$HTTP_CODE" == "201" ]]; then
    REVIEW_ID=$(echo "$BODY" | jq -r '.review_id // empty')
    echo "  [OK] tenant=$tid review_id=$REVIEW_ID period=$QUARTER"
    SUCCESS=$((SUCCESS + 1))
  else
    echo "  [FAIL] tenant=$tid http=$HTTP_CODE body=$BODY"
    FAILED=$((FAILED + 1))
  fi
done

echo ""
echo "[generate-access-review] Done. success=$SUCCESS failed=$FAILED"
[[ $FAILED -eq 0 ]]
