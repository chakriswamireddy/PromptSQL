#!/usr/bin/env bash
# kafka-topics-setup.sh
# Creates audit pipeline Kafka topics with the partition counts and retention
# settings defined in the Phase 5 plan.
# Usage: KAFKA_BROKERS=localhost:9092 ENV=local ./scripts/kafka-topics-setup.sh
set -euo pipefail

BROKERS="${KAFKA_BROKERS:-localhost:9092}"
ENV="${ENV:-local}"
KAFKA_CMD="${KAFKA_CMD:-kafka-topics.sh}"

create_topic() {
  local name="$1"
  local partitions="$2"
  local retention_ms="$3"

  echo "Creating topic: ${name} (partitions=${partitions}, retention=${retention_ms}ms)"
  "$KAFKA_CMD" --bootstrap-server "$BROKERS" \
    --create \
    --if-not-exists \
    --topic "$name" \
    --partitions "$partitions" \
    --replication-factor 1 \
    --config "retention.ms=${retention_ms}" \
    --config "compression.type=zstd" \
    --config "min.insync.replicas=1"
}

# Retention: 7 days = 604800000 ms
SEVEN_DAYS=604800000
# Retention: 30 days = 2592000000 ms
THIRTY_DAYS=2592000000

# ── Audit topics ──────────────────────────────────────────────────────────────
# Partition counts follow the plan (24/96/6); in local/dev single-node Kafka
# these are the same — scale up in prod.
create_topic "audit.policy.${ENV}"  24 "$SEVEN_DAYS"
create_topic "audit.access.${ENV}"  96 "$SEVEN_DAYS"
create_topic "audit.system.${ENV}"   6 "$THIRTY_DAYS"

# ── Dead-letter queues ────────────────────────────────────────────────────────
create_topic "audit.policy.${ENV}.dlq.clickhouse"  4 "$SEVEN_DAYS"
create_topic "audit.access.${ENV}.dlq.clickhouse"  4 "$SEVEN_DAYS"
create_topic "audit.policy.${ENV}.dlq.worm"        4 "$SEVEN_DAYS"
create_topic "audit.access.${ENV}.dlq.worm"        4 "$SEVEN_DAYS"

echo ""
echo "✓ All audit topics created for environment: ${ENV}"
"$KAFKA_CMD" --bootstrap-server "$BROKERS" --list | grep "^audit\."
