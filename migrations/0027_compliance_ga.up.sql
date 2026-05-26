-- Phase 16: Compliance, Hardening & GA Launch
-- Forward-only migration. No down migration in production.

-- ── compliance_modes — per-tenant compliance opt-ins and settings ─────────────
CREATE TABLE IF NOT EXISTS compliance_modes (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    hipaa_enabled        boolean     NOT NULL DEFAULT false,
    soc2_enabled         boolean     NOT NULL DEFAULT true,
    iso27001_enabled     boolean     NOT NULL DEFAULT false,
    pci_enabled          boolean     NOT NULL DEFAULT false,
    gdpr_enabled         boolean     NOT NULL DEFAULT true,
    data_retention_days  integer     NOT NULL DEFAULT 365,
    cmk_key_arn          text,
    baa_signed_at        timestamptz,
    baa_signed_by        text,
    mfa_required         boolean     NOT NULL DEFAULT false,
    sso_required         boolean     NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE(tenant_id)
);

ALTER TABLE compliance_modes ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_modes FORCE ROW LEVEL SECURITY;

CREATE POLICY compliance_modes_tenant_isolation ON compliance_modes
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

COMMENT ON TABLE  compliance_modes IS 'Per-tenant compliance feature configuration';
COMMENT ON COLUMN compliance_modes.cmk_key_arn IS 'Customer-managed KMS key ARN for HIPAA column encryption';
COMMENT ON COLUMN compliance_modes.baa_signed_at IS 'Timestamp when BAA was countersigned for HIPAA opt-in';

-- ── subprocessors — public trust-page sub-processor list ─────────────────────
CREATE TABLE IF NOT EXISTS subprocessors (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    purpose     text        NOT NULL,
    location    text        NOT NULL,
    data_types  text[]      NOT NULL DEFAULT '{}',
    dpa_url     text,
    active      boolean     NOT NULL DEFAULT true,
    added_at    timestamptz NOT NULL DEFAULT now(),
    removed_at  timestamptz
);

COMMENT ON TABLE subprocessors IS 'Sub-processors listed on the public trust page';

-- Seed well-known sub-processors
INSERT INTO subprocessors (name, purpose, location, data_types, active) VALUES
    ('Amazon Web Services',    'Infrastructure (compute, storage, networking)',        'United States / EU',       ARRAY['all'],                              true),
    ('Stripe',                 'Payment processing',                                  'United States',             ARRAY['billing'],                          true),
    ('Anthropic',              'AI policy authoring and NL-to-SQL (LLM inference)',   'United States',             ARRAY['query_text','policy_text'],         true),
    ('OpenAI',                 'Fallback LLM inference',                              'United States',             ARRAY['query_text','policy_text'],         true),
    ('Confluent / MSK',        'Kafka managed streaming',                             'United States / EU',        ARRAY['audit_events','system_events'],      true),
    ('ClickHouse Cloud',       'Audit analytics',                                     'United States / EU',        ARRAY['audit_events'],                     true),
    ('HashiCorp Vault',        'Secrets management',                                  'United States',             ARRAY['secrets'],                          true),
    ('PagerDuty',              'On-call alerting',                                    'United States',             ARRAY['alert_metadata'],                   true)
ON CONFLICT DO NOTHING;

-- ── compliance_evidence — automated evidence collection ledger ────────────────
CREATE TABLE IF NOT EXISTS compliance_evidence (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    control_id     text        NOT NULL,
    framework      text        NOT NULL CHECK (framework IN ('SOC2','ISO27001','HIPAA','GDPR','PCI')),
    evidence_type  text        NOT NULL CHECK (evidence_type IN ('access_review','log_retention','encryption','incident','change_management','vendor','training','vulnerability_scan','penetration_test')),
    collected_at   timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz,
    status         text        NOT NULL DEFAULT 'valid' CHECK (status IN ('valid','stale','missing','exempt')),
    evidence_ref   text        NOT NULL,
    collected_by   text        NOT NULL DEFAULT 'system',
    metadata       jsonb       NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE compliance_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE compliance_evidence FORCE ROW LEVEL SECURITY;

CREATE POLICY compliance_evidence_tenant_isolation ON compliance_evidence
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

CREATE INDEX IF NOT EXISTS idx_compliance_evidence_tenant_framework
    ON compliance_evidence(tenant_id, framework, status);

CREATE INDEX IF NOT EXISTS idx_compliance_evidence_expires
    ON compliance_evidence(expires_at) WHERE expires_at IS NOT NULL;

COMMENT ON TABLE compliance_evidence IS 'Automated SOC 2 / ISO 27001 evidence collection records';

-- ── access_reviews — quarterly user×role certification records ────────────────
CREATE TABLE IF NOT EXISTS access_reviews (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    review_period   text        NOT NULL,
    generated_at    timestamptz NOT NULL DEFAULT now(),
    due_at          timestamptz NOT NULL,
    completed_at    timestamptz,
    total_entries   integer     NOT NULL DEFAULT 0,
    certified_count integer     NOT NULL DEFAULT 0,
    revoked_count   integer     NOT NULL DEFAULT 0,
    status          text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','in_progress','completed','overdue')),
    generated_by    text        NOT NULL DEFAULT 'system',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, review_period)
);

ALTER TABLE access_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_reviews FORCE ROW LEVEL SECURITY;

CREATE POLICY access_reviews_tenant_isolation ON access_reviews
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

-- ── access_review_entries — per-user-role certification decisions ─────────────
CREATE TABLE IF NOT EXISTS access_review_entries (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    review_id      uuid        NOT NULL REFERENCES access_reviews(id) ON DELETE CASCADE,
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id        uuid        NOT NULL,
    user_email     text        NOT NULL,
    role           text        NOT NULL,
    last_active_at timestamptz,
    certified_by   uuid,
    certified_at   timestamptz,
    decision       text        CHECK (decision IN ('certify','revoke','pending')),
    notes          text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE access_review_entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE access_review_entries FORCE ROW LEVEL SECURITY;

CREATE POLICY access_review_entries_tenant_isolation ON access_review_entries
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

CREATE INDEX IF NOT EXISTS idx_access_review_entries_review
    ON access_review_entries(review_id, tenant_id);

CREATE INDEX IF NOT EXISTS idx_access_review_entries_pending
    ON access_review_entries(tenant_id, decision) WHERE decision = 'pending';

-- ── customer_health_scores — daily CS health rollup ──────────────────────────
CREATE TABLE IF NOT EXISTS customer_health_scores (
    id               bigserial    PRIMARY KEY,
    tenant_id        uuid         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    score_date       date         NOT NULL DEFAULT CURRENT_DATE,
    health_score     numeric(5,2) NOT NULL CHECK (health_score BETWEEN 0 AND 100),
    queries_per_day  integer      NOT NULL DEFAULT 0,
    active_users_7d  integer      NOT NULL DEFAULT 0,
    policy_count     integer      NOT NULL DEFAULT 0,
    ai_queries_7d    integer      NOT NULL DEFAULT 0,
    anomalies_7d     integer      NOT NULL DEFAULT 0,
    risk_events_7d   integer      NOT NULL DEFAULT 0,
    computed_at      timestamptz  NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, score_date)
);

ALTER TABLE customer_health_scores ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_health_scores FORCE ROW LEVEL SECURITY;

CREATE POLICY customer_health_scores_tenant_isolation ON customer_health_scores
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

CREATE INDEX IF NOT EXISTS idx_health_scores_tenant_date
    ON customer_health_scores(tenant_id, score_date DESC);

-- ── gdpr_sar_requests — GDPR Subject Access / Erasure Requests ───────────────
CREATE TABLE IF NOT EXISTS gdpr_sar_requests (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    subject_email  text        NOT NULL,
    request_type   text        NOT NULL CHECK (request_type IN ('access','erasure','portability','rectification')),
    status         text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','completed','rejected')),
    submitted_at   timestamptz NOT NULL DEFAULT now(),
    due_at         timestamptz NOT NULL DEFAULT now() + interval '30 days',
    completed_at   timestamptz,
    processed_by   uuid,
    download_url   text,
    notes          text,
    created_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE gdpr_sar_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE gdpr_sar_requests FORCE ROW LEVEL SECURITY;

CREATE POLICY gdpr_sar_requests_tenant_isolation ON gdpr_sar_requests
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

CREATE INDEX IF NOT EXISTS idx_gdpr_sar_tenant_status
    ON gdpr_sar_requests(tenant_id, status, due_at);

-- ── scim_tokens — SCIM 2.0 provisioning authentication tokens ────────────────
CREATE TABLE IF NOT EXISTS scim_tokens (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    token_hash  text        NOT NULL,
    label       text        NOT NULL,
    created_by  uuid        NOT NULL,
    last_used_at timestamptz,
    expires_at  timestamptz,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE scim_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE scim_tokens FORCE ROW LEVEL SECURITY;

CREATE POLICY scim_tokens_tenant_isolation ON scim_tokens
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

-- ── billing_subscriptions — Stripe subscription mirror ───────────────────────
CREATE TABLE IF NOT EXISTS billing_subscriptions (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    stripe_customer_id    text        NOT NULL,
    stripe_sub_id         text        NOT NULL,
    plan_tier             text        NOT NULL CHECK (plan_tier IN ('starter','pro','enterprise','design_partner')),
    status                text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','past_due','canceled','paused','trialing')),
    current_period_start  timestamptz NOT NULL,
    current_period_end    timestamptz NOT NULL,
    cancel_at_period_end  boolean     NOT NULL DEFAULT false,
    ai_usage_units        bigint      NOT NULL DEFAULT 0,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE(tenant_id),
    UNIQUE(stripe_sub_id)
);

ALTER TABLE billing_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_subscriptions FORCE ROW LEVEL SECURITY;

CREATE POLICY billing_subscriptions_tenant_isolation ON billing_subscriptions
    USING (tenant_id = current_setting('app.current_tenant_id')::uuid);

COMMENT ON TABLE billing_subscriptions IS 'Mirror of Stripe subscription state; source-of-truth is Stripe';

-- ── Grant roles ───────────────────────────────────────────────────────────────
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_read') THEN
    GRANT SELECT ON
        compliance_modes, subprocessors, compliance_evidence,
        access_reviews, access_review_entries, customer_health_scores,
        gdpr_sar_requests, scim_tokens, billing_subscriptions
      TO app_read;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_write') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON
        compliance_modes, subprocessors, compliance_evidence,
        access_reviews, access_review_entries, customer_health_scores,
        gdpr_sar_requests, scim_tokens, billing_subscriptions
      TO app_write;
    GRANT USAGE, SELECT ON SEQUENCE customer_health_scores_id_seq TO app_write;
  END IF;
END $$;
