#!/usr/bin/env bash
# chaos.sh — randomly kill one compose service to verify reconnection logic.
# Only for local dev; never run in staging/prod.
set -euo pipefail

SERVICES=(postgres redis kafka vault)
VICTIM="${SERVICES[$((RANDOM % ${#SERVICES[@]}))]}"

echo "[chaos] Killing service: $VICTIM for 15s..."
docker compose -f infra/docker-compose.yml stop "$VICTIM"
sleep 15
docker compose -f infra/docker-compose.yml start "$VICTIM"
echo "[chaos] Service $VICTIM restarted. Check your app logs."
