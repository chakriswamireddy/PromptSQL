#!/usr/bin/env bash
# Synthetic probe: PDP decide endpoint
# Runs every 60s per region via CronJob or external monitoring system.
# Exits 0 on success, 1 on failure (triggers alerting).
#
# Usage: REGION=us-east-1 JWT_TOKEN=... ./probe-pdp.sh

set -euo pipefail

REGION="${REGION:-us-east-1}"
BASE_URL="${BASE_URL:-https://api.governance-platform.io}"
JWT_TOKEN="${JWT_TOKEN:?JWT_TOKEN required}"
TIMEOUT_S=5

PAYLOAD=$(cat <<'JSON'
{
  "subject":  {"id": "probe-user", "roles": ["analyst"]},
  "resource": {"type": "table", "id": "probe-table", "tenant_id": "00000000-0000-0000-0000-000000000000"},
  "action":   "select",
  "context":  {"risk_score": 10}
}
JSON
)

START=$(date +%s%N)

RESPONSE=$(curl -sf \
  --max-time "$TIMEOUT_S" \
  -X POST "${BASE_URL}/v1/pdp/decide" \
  -H "Authorization: Bearer ${JWT_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "X-Janus-Tenant-ID: 00000000-0000-0000-0000-000000000000" \
  -w "\n%{http_code}" \
  -d "$PAYLOAD" 2>&1) || {
    echo "probe_pdp_ok{region=\"${REGION}\"} 0" \
      >> /var/governance/probes/metrics.prom
    echo "FAIL: curl error for ${BASE_URL}/v1/pdp/decide" >&2
    exit 1
  }

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | head -n -1)
END=$(date +%s%N)
LATENCY_MS=$(( (END - START) / 1000000 ))

if [[ "$HTTP_CODE" != "200" ]]; then
  echo "probe_pdp_ok{region=\"${REGION}\"} 0" \
    >> /var/governance/probes/metrics.prom
  echo "FAIL: PDP returned HTTP ${HTTP_CODE}: ${BODY}" >&2
  exit 1
fi

DECISION=$(echo "$BODY" | jq -r '.decision // empty' 2>/dev/null)
if [[ -z "$DECISION" ]]; then
  echo "probe_pdp_ok{region=\"${REGION}\"} 0" \
    >> /var/governance/probes/metrics.prom
  echo "FAIL: PDP response missing .decision field" >&2
  exit 1
fi

# Write Prometheus textfile metrics for node-exporter or pushgateway.
cat >> /var/governance/probes/metrics.prom <<EOF
probe_pdp_ok{region="${REGION}"} 1
probe_pdp_latency_ms{region="${REGION}"} ${LATENCY_MS}
EOF

echo "OK: PDP decide=${DECISION} latency=${LATENCY_MS}ms region=${REGION}"
