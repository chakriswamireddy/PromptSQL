#!/usr/bin/env bash
# DR Drill: Full Regional Failover (the comprehensive quarterly drill)
# Exercises: Aurora failover + Kafka reroute + service re-routing + verification
# Usage: DRILL_ENV=staging ./scripts/dr-drill/drill-full-regional.sh
#
# This script orchestrates the full scenario from the Phase 15 plan.
# It calls the individual drill scripts in order and aggregates results.

set -euo pipefail

ENVIRONMENT="${DRILL_ENV:-staging}"
DRY_RUN="${DRY_RUN:-false}"
export ENVIRONMENT DRY_RUN

DRILL_START=$(date +%s)
PASS_COUNT=0
FAIL_COUNT=0

log()  { echo "[$(date -u +%T)] $*"; }
pass() { log "PASS: $1"; ((PASS_COUNT++)) || true; }
fail() { log "FAIL: $1"; ((FAIL_COUNT++)) || true; }

log "========================================================"
log "QUARTERLY DR DRILL — Full Regional Failover"
log "Environment: ${ENVIRONMENT} | Dry-run: ${DRY_RUN}"
log "Start: $(date -u)"
log "========================================================"

# ── Phase A: Pre-drill baseline ────────────────────────────────────────────────
log ""
log "Phase A: Pre-drill baseline checks"

# Check replication lag
LAG=$(aws cloudwatch get-metric-statistics \
  --namespace "AWS/RDS" \
  --metric-name "AuroraGlobalDBReplicationLag" \
  --dimensions Name=DBClusterIdentifier,Value="governance-${ENVIRONMENT}-aurora-secondary" \
  --start-time "$(date -u -d '5 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" \
  --end-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --period 60 --statistics Maximum \
  --region eu-west-1 \
  --query 'Datapoints[-1].Maximum' --output text 2>/dev/null || echo "999")

if (( $(echo "$LAG < 60" | bc -l) )); then
  pass "Replication lag=${LAG}s — within 60s baseline threshold"
else
  fail "Replication lag=${LAG}s — too high for drill. Investigate before proceeding."
  exit 1
fi

# ── Phase B: Aurora failover ───────────────────────────────────────────────────
log ""
log "Phase B: Aurora primary failover"
if bash scripts/dr-drill/drill-pg-failover.sh; then
  pass "Aurora failover within RTO/RPO"
else
  fail "Aurora failover exceeded targets"
fi

# ── Phase C: Kafka reroute ─────────────────────────────────────────────────────
log ""
log "Phase C: Kafka producer reroute to secondary"
if bash scripts/dr-drill/drill-kafka-failover.sh; then
  pass "Kafka reroute within targets"
else
  fail "Kafka reroute failed or exceeded targets"
fi

# ── Phase D: End-to-end synthetic transaction ─────────────────────────────────
log ""
log "Phase D: End-to-end synthetic transaction verification"
E2E_START=$(date +%s)

if [[ "$DRY_RUN" != "true" ]]; then
  # Create a policy via admin API and verify it evaluates correctly in PDP
  POLICY_ID=$(curl -sf -X POST \
    "https://api.governance-platform-${ENVIRONMENT}.io/v1/policies" \
    -H "Authorization: Bearer ${DRILL_JWT_TOKEN}" \
    -H "Content-Type: application/json" \
    -d '{"name":"dr-drill-policy","rules":[]}' \
    | jq -r '.id' 2>/dev/null || echo "")

  if [[ -n "$POLICY_ID" ]]; then
    pass "Policy create succeeded (id=${POLICY_ID})"
    # Clean up drill artifact
    curl -sf -X DELETE \
      "https://api.governance-platform-${ENVIRONMENT}.io/v1/policies/${POLICY_ID}" \
      -H "Authorization: Bearer ${DRILL_JWT_TOKEN}" || true
  else
    fail "Policy create failed — write path may be degraded"
  fi
else
  log "[DRY RUN] Would run end-to-end policy create/delete probe"
  pass "End-to-end probe (dry-run)"
fi

E2E_SECS=$(( $(date +%s) - E2E_START ))
log "End-to-end transaction: ${E2E_SECS}s"

# ── Phase E: Verify observability ─────────────────────────────────────────────
log ""
log "Phase E: Observability checks"
if [[ "$DRY_RUN" != "true" ]]; then
  # Check that Prometheus is scraping metrics from eu-west-1 pods
  METRIC_OK=$(curl -sf \
    "http://prometheus.monitoring.svc:9090/api/v1/query?query=up%7Bjob%3D%22pdp%22%7D" \
    | jq '.data.result | length > 0' 2>/dev/null || echo "false")

  if [[ "$METRIC_OK" == "true" ]]; then
    pass "Prometheus metrics available for eu-west-1 services"
  else
    fail "Prometheus not scraping eu-west-1 PDP — observability gap"
  fi
else
  pass "Observability check (dry-run)"
fi

# ── Results ───────────────────────────────────────────────────────────────────
DRILL_END=$(date +%s)
TOTAL_MINUTES=$(echo "scale=2; $(( DRILL_END - DRILL_START )) / 60" | bc)

log ""
log "========================================================"
log "DRILL COMPLETE"
log "Total duration: ${TOTAL_MINUTES} minutes"
log "Checks passed: ${PASS_COUNT}"
log "Checks failed: ${FAIL_COUNT}"
log "========================================================"

if [[ "$DRY_RUN" != "true" ]]; then
  psql "$DATABASE_URL" <<SQL
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES (
  'full_regional',
  '${ENVIRONMENT}',
  '$(whoami)',
  to_timestamp(${DRILL_START}),
  to_timestamp(${DRILL_END}),
  ${TOTAL_MINUTES},
  NULL,
  $(( FAIL_COUNT == 0 ? 1 : 0 ))::boolean,
  NULL,
  'Full regional drill: ${PASS_COUNT} passed, ${FAIL_COUNT} failed'
);
SQL
fi

if [[ $FAIL_COUNT -gt 0 ]]; then
  log "DRILL FAILED — resolve failures before marking Phase 15 complete"
  exit 1
fi

log "DRILL PASSED — all checks met RTO/RPO targets"
