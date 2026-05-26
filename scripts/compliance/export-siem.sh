#!/usr/bin/env bash
# Export audit events in SIEM-compatible format (CEF or JSON).
# Usage: ./export-siem.sh --tenant-id <id> [--format cef|json] [--hours 24] [--output /path/to/file]
#
# Requires: curl, GOVERNANCE_API_URL, GOVERNANCE_SERVICE_TOKEN env vars.

set -euo pipefail

API_URL="${GOVERNANCE_API_URL:-http://localhost:8080}"
TOKEN="${GOVERNANCE_SERVICE_TOKEN:-}"
TENANT_ID=""
FORMAT="json"
HOURS=24
OUTPUT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --tenant-id) TENANT_ID="$2"; shift 2 ;;
    --format)    FORMAT="$2";    shift 2 ;;
    --hours)     HOURS="$2";     shift 2 ;;
    --output)    OUTPUT="$2";    shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

if [[ -z "$TENANT_ID" ]]; then
  echo "Usage: $0 --tenant-id <id> [--format cef|json] [--hours 24] [--output /path]"
  exit 1
fi

FROM=$(date -u -d "-${HOURS} hours" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -v-${HOURS}H +"%Y-%m-%dT%H:%M:%SZ")
TO=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

URL="$API_URL/v1/admin/$TENANT_ID/audit/export/siem?format=$FORMAT&from=$FROM&to=$TO"

AUTH_ARGS=()
if [[ -n "$TOKEN" ]]; then
  AUTH_ARGS=("-H" "Authorization: Bearer $TOKEN")
fi

echo "[export-siem] tenant=$TENANT_ID format=$FORMAT from=$FROM to=$TO"

if [[ -n "$OUTPUT" ]]; then
  curl -sf "${AUTH_ARGS[@]}" "$URL" -o "$OUTPUT"
  echo "[export-siem] Written to $OUTPUT ($(wc -l < "$OUTPUT") lines)"
else
  curl -sf "${AUTH_ARGS[@]}" "$URL"
fi
