#!/usr/bin/env bash
# verify-env.sh — smoke-test the local (or remote) dev environment.
# Exits 0 only when all required services are healthy.
set -euo pipefail

ENV="${1:---env local}"
PASS=0; FAIL=0

check() {
  local NAME="$1"; shift
  if "$@" > /dev/null 2>&1; then
    echo "  [OK]  $NAME"
    PASS=$((PASS+1))
  else
    echo "  [FAIL] $NAME"
    FAIL=$((FAIL+1))
  fi
}

echo "=== Governance Platform — Environment Doctor ==="
echo ""

echo "── Services ─────────────────────────────────────────────"
check "Postgres"   pg_isready -h 127.0.0.1 -p "${POSTGRES_PORT:-5432}" -U "${POSTGRES_USER:-app}"
check "Redis"      redis-cli -h 127.0.0.1 -p "${REDIS_PORT:-6379}" ping
check "Vault"      vault status
check "Unleash"    curl -sf "http://127.0.0.1:${UNLEASH_PORT:-4242}/health"
check "Jaeger UI"  curl -sf "http://127.0.0.1:${JAEGER_UI_PORT:-16686}/"
check "Prometheus" curl -sf "http://127.0.0.1:${PROMETHEUS_PORT:-9090}/-/ready"
check "Grafana"    curl -sf "http://127.0.0.1:${GRAFANA_PORT:-3001}/api/health"
check "MinIO"      curl -sf "http://127.0.0.1:${MINIO_PORT:-9000}/minio/health/live"
check "OIDC Mock"  curl -sf "http://127.0.0.1:${OIDC_PORT:-9080}/.well-known/openid-configuration"

echo ""
echo "── Ports ────────────────────────────────────────────────"
for PORT in "${POSTGRES_PORT:-5432}" "${REDIS_PORT:-6379}" "${KAFKA_PORT:-9092}" "${VAULT_PORT:-8200}"; do
  if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
    echo "  [OK]  :$PORT open"
  else
    echo "  [WARN] :$PORT not reachable (service may be in minimal profile)"
  fi
done

echo ""
echo "=== Result: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
