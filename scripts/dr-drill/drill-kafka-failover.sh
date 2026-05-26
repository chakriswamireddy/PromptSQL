#!/usr/bin/env bash
# DR Drill: Kafka Producer Reroute
# Simulates MSK primary loss by pointing producers at the eu-west-1 broker.
# Usage: DRILL_ENV=staging ./scripts/dr-drill/drill-kafka-failover.sh

set -euo pipefail

ENVIRONMENT="${DRILL_ENV:-staging}"
DRY_RUN="${DRY_RUN:-false}"
START_TIME=$(date +%s)

log()  { echo "[$(date -u +%T)] $*"; }
fail() { log "FAIL: $*" >&2; exit 1; }

log "=== DR DRILL: Kafka Failover — ${ENVIRONMENT} ==="

EU_KAFKA="${EU_KAFKA_BOOTSTRAP:?EU_KAFKA_BOOTSTRAP must be set}"
PRIMARY_CTX="governance-${ENVIRONMENT}-us-east-1"
SECONDARY_CTX="governance-${ENVIRONMENT}-eu-west-1"

# ── Record pre-drill Kafka lag ─────────────────────────────────────────────────
log "Pre-drill: measuring consumer group lag..."
if [[ "$DRY_RUN" != "true" ]]; then
  PRE_LAG=$(kubectl --context "$PRIMARY_CTX" exec -n governance-platform \
    deployment/audit-clickhouse-sink -- \
    kafka-consumer-groups.sh \
      --bootstrap-server "$EU_KAFKA" \
      --group audit-clickhouse-sink \
      --describe 2>/dev/null | awk 'NR>1{sum+=$6} END{print sum+0}' || echo "0")
else
  PRE_LAG=0
fi
log "Pre-drill consumer lag: ${PRE_LAG} messages"

# ── Reroute producers ─────────────────────────────────────────────────────────
log "Rerouting producers to eu-west-1 Kafka..."
REROUTE_START=$(date +%s)

SERVICES=(api-gateway anomaly-detector auto-responder)
for SVC in "${SERVICES[@]}"; do
  if [[ "$DRY_RUN" != "true" ]]; then
    kubectl --context "$SECONDARY_CTX" \
      set env deployment/$SVC KAFKA_BROKERS="${EU_KAFKA}" \
      -n governance-platform
    kubectl --context "$SECONDARY_CTX" rollout restart deployment/$SVC \
      -n governance-platform
  else
    log "[DRY RUN] Would reroute $SVC to ${EU_KAFKA}"
  fi
done

# Wait for rollouts
if [[ "$DRY_RUN" != "true" ]]; then
  for SVC in "${SERVICES[@]}"; do
    kubectl --context "$SECONDARY_CTX" rollout status deployment/$SVC \
      -n governance-platform --timeout=120s
  done
fi

REROUTE_END=$(date +%s)
log "All producers rerouted ($(( REROUTE_END - REROUTE_START ))s)"

# ── Verify events flowing on eu-west-1 broker ─────────────────────────────────
log "Verifying events flowing on eu-west-1..."
sleep 10  # allow producers to send a batch

if [[ "$DRY_RUN" != "true" ]]; then
  POST_LAG=$(kubectl --context "$SECONDARY_CTX" exec -n governance-platform \
    deployment/audit-clickhouse-sink -- \
    kafka-consumer-groups.sh \
      --bootstrap-server "$EU_KAFKA" \
      --group audit-clickhouse-sink \
      --describe 2>/dev/null | awk 'NR>1{sum+=$6} END{print sum+0}' || echo "999")

  if (( POST_LAG < 1000 )); then
    log "Consumer lag acceptable: ${POST_LAG} messages"
  else
    fail "Consumer lag too high after reroute: ${POST_LAG} messages"
  fi
fi

# ── Measure RTO ───────────────────────────────────────────────────────────────
END_TIME=$(date +%s)
RTO_SECONDS=$(( END_TIME - REROUTE_START ))
RTO_MINUTES=$(echo "scale=2; $RTO_SECONDS / 60" | bc)
RTO_MET="false"
(( $(echo "$RTO_MINUTES < 30" | bc -l) )) && RTO_MET="true"

log "=== DRILL RESULTS ==="
log "RTO: ${RTO_MINUTES} minutes — met: ${RTO_MET}"

if [[ "$DRY_RUN" != "true" ]]; then
  psql "$DATABASE_URL" <<SQL
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES (
  'kafka_failover', '${ENVIRONMENT}', '$(whoami)',
  to_timestamp(${REROUTE_START}), to_timestamp(${END_TIME}),
  ${RTO_MINUTES}, NULL, ${RTO_MET}, NULL,
  'Pre-drill lag=${PRE_LAG} messages'
);
SQL
fi

[[ "$RTO_MET" == "true" ]] || fail "RTO target not met"
log "=== DRILL PASSED ==="
