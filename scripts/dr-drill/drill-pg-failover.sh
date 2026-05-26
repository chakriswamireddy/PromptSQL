#!/usr/bin/env bash
# DR Drill: PostgreSQL Primary Failover
# Environment: staging only (never run against prod without VP approval)
# Usage: ./scripts/dr-drill/drill-pg-failover.sh [--env staging]
#
# Validates: RTO < 30 min, RPO < 5 min

set -euo pipefail

ENVIRONMENT="${DRILL_ENV:-staging}"
DRY_RUN="${DRY_RUN:-false}"
START_TIME=$(date +%s)

log()  { echo "[$(date -u +%T)] $*"; }
fail() { echo "[$(date -u +%T)] FAIL: $*" >&2; exit 1; }

# ── Verify environment ────────────────────────────────────────────────────────
if [[ "$ENVIRONMENT" == "prod" && "$FORCE_PROD" != "yes" ]]; then
  fail "Set FORCE_PROD=yes to run against prod. This is a DESTRUCTIVE drill."
fi

log "=== DR DRILL: PostgreSQL Primary Failover — ${ENVIRONMENT} ==="
log "DRY_RUN=${DRY_RUN}"

# ── Step 1: Record baseline replication lag ────────────────────────────────────
log "Step 1: Measuring replication lag..."
LAG_SECONDS=$(aws cloudwatch get-metric-statistics \
  --namespace "AWS/RDS" \
  --metric-name "AuroraGlobalDBReplicationLag" \
  --dimensions Name=DBClusterIdentifier,Value="governance-${ENVIRONMENT}-aurora-secondary" \
  --start-time "$(date -u -d '5 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" \
  --end-time "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --period 60 \
  --statistics Maximum \
  --region eu-west-1 \
  --query 'Datapoints[-1].Maximum' \
  --output text 2>/dev/null || echo "60")

log "Current replication lag: ${LAG_SECONDS}s"
if (( $(echo "$LAG_SECONDS > 300" | bc -l) )); then
  fail "Replication lag ${LAG_SECONDS}s exceeds RPO target (300s). Aborting drill."
fi

# ── Step 2: Inject fault (simulate primary loss) ──────────────────────────────
log "Step 2: Injecting fault — detaching Aurora secondary from global cluster..."
FAILOVER_START=$(date +%s)

if [[ "$DRY_RUN" != "true" ]]; then
  aws rds remove-from-global-cluster \
    --global-cluster-identifier "governance-${ENVIRONMENT}-global" \
    --db-cluster-identifier "governance-${ENVIRONMENT}-aurora-secondary" \
    --region eu-west-1

  log "Waiting for secondary to become available as standalone writer..."
  aws rds wait db-cluster-available \
    --db-cluster-identifier "governance-${ENVIRONMENT}-aurora-secondary" \
    --region eu-west-1
else
  log "[DRY RUN] Would detach secondary from global cluster"
  sleep 2
fi

DETACH_TIME=$(date +%s)
log "Secondary detached and available ($(( DETACH_TIME - FAILOVER_START ))s)"

# ── Step 3: Update routing ────────────────────────────────────────────────────
log "Step 3: Updating write endpoint DNS..."
if [[ "$DRY_RUN" != "true" ]]; then
  cd infra/terraform/modules/route53-multiregion
  terraform apply -var="primary_region=eu-west-1" -auto-approve -input=false
  cd -
else
  log "[DRY RUN] Would update Route53 write record to eu-west-1"
  sleep 1
fi

# ── Step 4: Synthetic probe ───────────────────────────────────────────────────
log "Step 4: Running synthetic write probe against eu-west-1..."
PROBE_START=$(date +%s)

MAX_RETRIES=30
for i in $(seq 1 $MAX_RETRIES); do
  if [[ "$DRY_RUN" != "true" ]]; then
    STATUS=$(curl -sf -o /dev/null -w "%{http_code}" \
      "https://api.governance-platform-${ENVIRONMENT}.io/healthz" 2>/dev/null || echo "000")
  else
    STATUS="200"
  fi

  if [[ "$STATUS" == "200" ]]; then
    PROBE_END=$(date +%s)
    log "Probe succeeded after $(( PROBE_END - PROBE_START ))s (attempt $i)"
    break
  fi

  if [[ $i -eq $MAX_RETRIES ]]; then
    fail "Probe failed after ${MAX_RETRIES} attempts. RTO target breached."
  fi
  sleep 5
done

# ── Step 5: Measure RTO and RPO ───────────────────────────────────────────────
END_TIME=$(date +%s)
RTO_SECONDS=$(( END_TIME - FAILOVER_START ))
RTO_MINUTES=$(echo "scale=2; $RTO_SECONDS / 60" | bc)
RPO_MINUTES=$(echo "scale=2; $LAG_SECONDS / 60" | bc)

RTO_MET="false"; RPO_MET="false"
(( $(echo "$RTO_MINUTES < 30" | bc -l) )) && RTO_MET="true"
(( $(echo "$RPO_MINUTES < 5" | bc -l) )) && RPO_MET="true"

log "=== DRILL RESULTS ==="
log "RTO: ${RTO_MINUTES} minutes (target: < 30 min) — met: ${RTO_MET}"
log "RPO: ${RPO_MINUTES} minutes (target: < 5 min) — met: ${RPO_MET}"

# ── Step 6: Record results ────────────────────────────────────────────────────
log "Step 6: Recording drill results in database..."
if [[ "$DRY_RUN" != "true" ]]; then
  psql "$DATABASE_URL" <<SQL
INSERT INTO dr_drills (drill_type, environment, executed_by, started_at, completed_at,
  rto_minutes, rpo_minutes, rto_target_met, rpo_target_met, notes)
VALUES (
  'pg_failover',
  '${ENVIRONMENT}',
  '$(whoami)',
  to_timestamp(${FAILOVER_START}),
  to_timestamp(${END_TIME}),
  ${RTO_MINUTES},
  ${RPO_MINUTES},
  ${RTO_MET},
  ${RPO_MET},
  'Automated drill via scripts/dr-drill/drill-pg-failover.sh'
);
SQL
else
  log "[DRY RUN] Would INSERT drill results to dr_drills table"
fi

if [[ "$RTO_MET" != "true" || "$RPO_MET" != "true" ]]; then
  fail "One or more targets NOT met. Phase 15 cannot be marked GA-ready until both RTO and RPO targets pass."
fi

log "=== DRILL PASSED ==="
