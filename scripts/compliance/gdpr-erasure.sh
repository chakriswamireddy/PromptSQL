#!/usr/bin/env bash
# Submit or process a GDPR Subject Access / Erasure Request.
# Usage: ./gdpr-erasure.sh --tenant-id <id> --email <email> --type erasure|access|portability

set -euo pipefail

API_URL="${GOVERNANCE_API_URL:-http://localhost:8080}"
TOKEN="${GOVERNANCE_SERVICE_TOKEN:-}"
TENANT_ID=""
SUBJECT_EMAIL=""
REQUEST_TYPE="erasure"
LIST=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tenant-id) TENANT_ID="$2";      shift 2 ;;
    --email)     SUBJECT_EMAIL="$2";  shift 2 ;;
    --type)      REQUEST_TYPE="$2";   shift 2 ;;
    --list)      LIST=true;           shift ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

AUTH_ARGS=()
if [[ -n "$TOKEN" ]]; then
  AUTH_ARGS=("-H" "Authorization: Bearer $TOKEN")
fi

if [[ -z "$TENANT_ID" ]]; then
  echo "Error: --tenant-id required"
  exit 1
fi

if [[ "$LIST" == "true" ]]; then
  echo "[gdpr] Listing requests for tenant $TENANT_ID"
  curl -sf "${AUTH_ARGS[@]}" "$API_URL/v1/admin/$TENANT_ID/gdpr/requests" | jq .
  exit 0
fi

if [[ -z "$SUBJECT_EMAIL" ]]; then
  echo "Error: --email required for submitting a request"
  exit 1
fi

echo "[gdpr] Submitting $REQUEST_TYPE request for $SUBJECT_EMAIL (tenant $TENANT_ID)"

RESPONSE=$(curl -sf -X POST \
  "${AUTH_ARGS[@]}" \
  -H "Content-Type: application/json" \
  -d "{\"subject_email\":\"$SUBJECT_EMAIL\",\"request_type\":\"$REQUEST_TYPE\"}" \
  "$API_URL/v1/admin/$TENANT_ID/gdpr/requests")

REQUEST_ID=$(echo "$RESPONSE" | jq -r '.request_id')
DUE=$(date -u -d "+30 days" +"%Y-%m-%d" 2>/dev/null || date -u -v+30d +"%Y-%m-%d")

echo "[gdpr] Request submitted. request_id=$REQUEST_ID due=$DUE"
echo ""
echo "Next steps:"
echo "  1. Export subject data from PostgreSQL, ClickHouse, and audit logs."
echo "  2. Package and upload to a signed S3 URL."
echo "  3. Update status: curl -X PUT '$API_URL/v1/admin/$TENANT_ID/gdpr/requests/$REQUEST_ID/status'"
echo "     -d '{\"status\":\"completed\",\"processed_by\":\"<admin_id>\",\"download_url\":\"<url>\"}'"
