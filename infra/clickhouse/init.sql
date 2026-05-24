-- ClickHouse audit schema — Phase 5
-- Applied automatically on container start via docker-entrypoint-initdb.d.
-- Tables use MergeTree family; ReplacingMergeTree for idempotent sinks.

-- ── audit_policy ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_policy
(
    event_id       UUID,
    tenant_id      UUID,
    actor_id       UUID,
    actor_token    String,                    -- HMAC-tokenized for GDPR
    action         LowCardinality(String),
    policy_id      UUID,
    before_json    String,
    after_json     String,
    request_id     UUID,
    trace_id       String,
    ip             IPv6,
    user_agent     String,
    schema_version LowCardinality(String) DEFAULT 'v1',
    event_time     DateTime64(3, 'UTC'),
    ingest_time    DateTime64(3, 'UTC') DEFAULT now64()
)
ENGINE = ReplacingMergeTree(ingest_time)
PARTITION BY toDate(event_time)
ORDER BY (tenant_id, event_time, event_id)
TTL toDate(event_time) + INTERVAL 7 YEAR
SETTINGS index_granularity = 8192;

-- ── audit_access ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_access
(
    event_id        UUID,
    tenant_id       UUID,
    user_id         UUID,
    actor_token     String,
    data_source_id  UUID,
    resource        String,
    action          LowCardinality(String),
    decision        LowCardinality(String),
    reason          String,
    row_count       Int64,
    query_hash      String,
    duration_ms     Int64,
    risk_score      Float64,
    break_glass     UInt8,
    policy_version  String,
    schema_version  LowCardinality(String) DEFAULT 'v1',
    event_time      DateTime64(3, 'UTC'),
    ingest_time     DateTime64(3, 'UTC') DEFAULT now64()
)
ENGINE = ReplacingMergeTree(ingest_time)
PARTITION BY toDate(event_time)
ORDER BY (tenant_id, user_id, event_time, event_id)
TTL toDate(event_time) + INTERVAL 7 YEAR
SETTINGS index_granularity = 8192;

-- ── audit_system ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS audit_system
(
    event_id       UUID,
    tenant_id      UUID,
    action         LowCardinality(String),
    detail         String,
    schema_version LowCardinality(String) DEFAULT 'v1',
    event_time     DateTime64(3, 'UTC'),
    ingest_time    DateTime64(3, 'UTC') DEFAULT now64()
)
ENGINE = ReplacingMergeTree(ingest_time)
PARTITION BY toDate(event_time)
ORDER BY (event_time, event_id)
TTL toDate(event_time) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192;

-- ── Skipping indices for common filter columns ────────────────────────────────
ALTER TABLE audit_access ADD INDEX IF NOT EXISTS idx_actor_token actor_token TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE audit_access ADD INDEX IF NOT EXISTS idx_resource resource TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE audit_policy ADD INDEX IF NOT EXISTS idx_actor_token actor_token TYPE bloom_filter(0.01) GRANULARITY 4;

-- ── Materialized view: decisions per tenant per hour ─────────────────────────
CREATE TABLE IF NOT EXISTS mv_decisions_per_tenant_hour_data
(
    tenant_id   UUID,
    hour        DateTime,
    decision    LowCardinality(String),
    cnt         UInt64
)
ENGINE = SummingMergeTree(cnt)
PARTITION BY toDate(hour)
ORDER BY (tenant_id, hour, decision);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_decisions_per_tenant_hour
TO mv_decisions_per_tenant_hour_data
AS SELECT
    tenant_id,
    toStartOfHour(event_time) AS hour,
    decision,
    count() AS cnt
FROM audit_access
GROUP BY tenant_id, hour, decision;

-- ── Materialized view: deny events per user per day ──────────────────────────
CREATE TABLE IF NOT EXISTS mv_denies_per_user_day_data
(
    tenant_id UUID,
    user_id   UUID,
    day       Date,
    cnt       UInt64
)
ENGINE = SummingMergeTree(cnt)
PARTITION BY day
ORDER BY (tenant_id, user_id, day);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_denies_per_user_day
TO mv_denies_per_user_day_data
AS SELECT
    tenant_id,
    user_id,
    toDate(event_time) AS day,
    count() AS cnt
FROM audit_access
WHERE decision = 'deny'
GROUP BY tenant_id, user_id, day;

-- ── Materialized view: p99 query latency per datasource per 5m ───────────────
CREATE TABLE IF NOT EXISTS mv_latency_p99_5m_data
(
    tenant_id      UUID,
    data_source_id UUID,
    window_start   DateTime,
    quantile_state AggregateFunction(quantile(0.99), Int64)
)
ENGINE = AggregatingMergeTree()
PARTITION BY toDate(window_start)
ORDER BY (tenant_id, data_source_id, window_start);

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_latency_p99_5m
TO mv_latency_p99_5m_data
AS SELECT
    tenant_id,
    data_source_id,
    toStartOfFiveMinutes(event_time) AS window_start,
    quantileState(0.99)(duration_ms) AS quantile_state
FROM audit_access
GROUP BY tenant_id, data_source_id, window_start;
