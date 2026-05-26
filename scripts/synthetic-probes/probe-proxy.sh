#!/usr/bin/env bash
# Synthetic probe: PostgreSQL proxy (pgproto3 wire protocol)
# Verifies the proxy can issue a db-token and complete a trivial query.
#
# Usage: REGION=us-east-1 JWT_TOKEN=... DATASOURCE_ID=... ./probe-proxy.sh

set -euo pipefail

REGION="${REGION:-us-east-1}"
BASE_URL="${BASE_URL:-https://api.governance-platform.io}"
JWT_TOKEN="${JWT_TOKEN:?JWT_TOKEN required}"
DATASOURCE_ID="${DATASOURCE_ID:?DATASOURCE_ID required}"
TIMEOUT_S=10

START=$(date +%s%N)

# Step 1: Obtain a scoped DB token
DB_TOKEN_RESPONSE=$(curl -sf \
  --max-time 5 \
  -X POST "${BASE_URL}/v1/db-token" \
  -H "Authorization: Bearer ${JWT_TOKEN}" \
  -H "Content-Type: application/json" \
  -H "X-Janus-Tenant-ID: 00000000-0000-0000-0000-000000000000" \
  -d "{\"datasource_id\": \"${DATASOURCE_ID}\"}" 2>&1) || {
    echo "probe_proxy_ok{region=\"${REGION}\"} 0" >> /var/governance/probes/metrics.prom
    echo "FAIL: db-token request failed" >&2; exit 1
  }

DB_TOKEN=$(echo "$DB_TOKEN_RESPONSE" | jq -r '.token // empty' 2>/dev/null)
PROXY_HOST=$(echo "$DB_TOKEN_RESPONSE" | jq -r '.host // empty' 2>/dev/null)
PROXY_PORT=$(echo "$DB_TOKEN_RESPONSE" | jq -r '.port // "5432"' 2>/dev/null)

if [[ -z "$DB_TOKEN" || -z "$PROXY_HOST" ]]; then
  echo "probe_proxy_ok{region=\"${REGION}\"} 0" >> /var/governance/probes/metrics.prom
  echo "FAIL: db-token response missing token/host" >&2; exit 1
fi

# Step 2: Execute a trivial SELECT via the proxy
RESULT=$(PGPASSWORD="$DB_TOKEN" psql \
  -h "$PROXY_HOST" -p "$PROXY_PORT" \
  -U "probe-user" \
  -d "governance" \
  -c "SELECT 1 AS probe" \
  -t --no-align \
  --connect-timeout 5 \
  2>&1) || {
    echo "probe_proxy_ok{region=\"${REGION}\"} 0" >> /var/governance/probes/metrics.prom
    echo "FAIL: psql connect/query failed: ${RESULT}" >&2; exit 1
  }

END=$(date +%s%N)
LATENCY_MS=$(( (END - START) / 1000000 ))

if [[ "$(echo "$RESULT" | tr -d ' ')" == "1" ]]; then
  cat >> /var/governance/probes/metrics.prom <<EOF
probe_proxy_ok{region="${REGION}"} 1
probe_proxy_latency_ms{region="${REGION}"} ${LATENCY_MS}
EOF
  echo "OK: proxy SELECT 1 latency=${LATENCY_MS}ms region=${REGION}"
else
  echo "probe_proxy_ok{region=\"${REGION}\"} 0" >> /var/governance/probes/metrics.prom
  echo "FAIL: unexpected proxy query result: ${RESULT}" >&2; exit 1
fi
