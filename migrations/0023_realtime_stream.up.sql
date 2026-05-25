-- Phase 12: Real-Time Event Stream & Live Access Feed
-- webhook_subscriptions, webhook_deliveries, webhook_dlq, saved_questions schedule columns

BEGIN;

-- ── Webhook subscriptions ─────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id              UUID        PRIMARY KEY DEFAULT gen_ulid(),
    tenant_id       UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name            TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    url             TEXT        NOT NULL CHECK (url ~ '^https://'),
    event_types     TEXT[]      NOT NULL DEFAULT '{}',
    secret_ref      TEXT        NOT NULL, -- Vault path, e.g. secret/data/webhooks/<id>
    field_allowlist TEXT[]      NOT NULL DEFAULT '{}', -- empty = all fields
    filter_expr     TEXT        NOT NULL DEFAULT '', -- bounded filter DSL, max 512 chars
    is_active       BOOLEAN     NOT NULL DEFAULT true,
    failure_count   INT         NOT NULL DEFAULT 0,
    last_delivery_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE webhook_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_subscriptions FORCE ROW LEVEL SECURITY;

CREATE POLICY webhook_subscriptions_tenant_isolation ON webhook_subscriptions
    USING (tenant_id = current_setting('app.current_tenant_id')::UUID);

CREATE INDEX IF NOT EXISTS webhook_subscriptions_tenant_idx ON webhook_subscriptions (tenant_id);
CREATE INDEX IF NOT EXISTS webhook_subscriptions_active_idx ON webhook_subscriptions (tenant_id, is_active) WHERE is_active;

-- ── Webhook deliveries ────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id               UUID        PRIMARY KEY DEFAULT gen_ulid(),
    subscription_id  UUID        NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_id         TEXT        NOT NULL,
    event_type       TEXT        NOT NULL,
    attempt          INT         NOT NULL DEFAULT 1,
    status           TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed','dlq')),
    status_code      INT,
    response_body    TEXT,
    duration_ms      INT,
    attempted_at     TIMESTAMPTZ,
    next_retry_at    TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partition by month for volume management.
CREATE INDEX IF NOT EXISTS webhook_deliveries_sub_idx  ON webhook_deliveries (subscription_id, created_at DESC);
CREATE INDEX IF NOT EXISTS webhook_deliveries_event_idx ON webhook_deliveries (event_id);
CREATE INDEX IF NOT EXISTS webhook_deliveries_retry_idx ON webhook_deliveries (next_retry_at) WHERE status = 'pending';

-- ── Webhook DLQ ───────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS webhook_dlq (
    id              UUID        PRIMARY KEY DEFAULT gen_ulid(),
    delivery_id     UUID        NOT NULL REFERENCES webhook_deliveries(id),
    subscription_id UUID        NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    tenant_id       UUID        NOT NULL,
    event_payload   JSONB       NOT NULL,
    last_error      TEXT,
    replayed_at     TIMESTAMPTZ,
    replayed_by     UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE webhook_dlq ENABLE ROW LEVEL SECURITY;
ALTER TABLE webhook_dlq FORCE ROW LEVEL SECURITY;

CREATE POLICY webhook_dlq_tenant_isolation ON webhook_dlq
    USING (tenant_id = current_setting('app.current_tenant_id')::UUID);

CREATE INDEX IF NOT EXISTS webhook_dlq_tenant_idx  ON webhook_dlq (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS webhook_dlq_sub_idx     ON webhook_dlq (subscription_id);

-- ── saved_questions schedule columns ─────────────────────────────────────────

ALTER TABLE saved_questions
    ADD COLUMN IF NOT EXISTS schedule_cron TEXT        CHECK (schedule_cron ~ '^(@(annually|yearly|monthly|weekly|daily|hourly)|(\*|([0-9]|[1-5][0-9])) (\*|([0-9]|1[0-9]|2[0-3])) (\*|([1-9]|[12][0-9]|3[01])) (\*|([1-9]|1[0-2])) (\*|[0-6]))$'),
    ADD COLUMN IF NOT EXISTS last_run_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS next_run_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS schedule_enabled BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS saved_questions_schedule_idx ON saved_questions (next_run_at)
    WHERE schedule_enabled AND next_run_at IS NOT NULL;

-- ── Update triggers for updated_at ───────────────────────────────────────────

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
$$;

DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'webhook_subscriptions_updated_at') THEN
    CREATE TRIGGER webhook_subscriptions_updated_at
        BEFORE UPDATE ON webhook_subscriptions
        FOR EACH ROW EXECUTE FUNCTION set_updated_at();
  END IF;
END $$;

COMMIT;
