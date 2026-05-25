-- Phase 14: Auto-Response, Step-Up Auth, Break-Glass
-- Forward-only migration; no down migration in production.

-- ── risk_playbooks ─────────────────────────────────────────────────────────────
-- Per-tenant configurable auto-response playbooks. Versioned + immutable once
-- activated; deactivate the old version before activating the next.
-- tiers: [{min,max,action,params}] where action ∈ {normal,tag,step_up,mask,block}
CREATE TABLE IF NOT EXISTS risk_playbooks (
    id                  uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id           uuid        NOT NULL,
    version             integer     NOT NULL DEFAULT 1,
    tiers               jsonb       NOT NULL DEFAULT '[
        {"min":0,  "max":40, "action":"normal",  "params":{}},
        {"min":41, "max":70, "action":"tag",     "params":{}},
        {"min":71, "max":85, "action":"step_up", "params":{"mfa_window_sec":300}},
        {"min":86, "max":95, "action":"mask",    "params":{}},
        {"min":96, "max":100,"action":"block",   "params":{}}
    ]'::jsonb,
    escalation_targets  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    active              boolean     NOT NULL DEFAULT false,
    pause_auto_response boolean     NOT NULL DEFAULT false,
    created_by          uuid        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    activated_at        timestamptz,
    UNIQUE (tenant_id, version)
);

ALTER TABLE risk_playbooks ENABLE ROW LEVEL SECURITY;
ALTER TABLE risk_playbooks FORCE ROW LEVEL SECURITY;

CREATE POLICY risk_playbooks_tenant_isolation ON risk_playbooks
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_risk_playbooks_tenant_active
    ON risk_playbooks (tenant_id)
    WHERE active = true;

-- ── breakglass_sessions ────────────────────────────────────────────────────────
-- Time-boxed emergency access bypass. Requires dual approval. Auto-expires.
CREATE TABLE IF NOT EXISTS breakglass_sessions (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id       uuid        NOT NULL,
    principal_id    uuid        NOT NULL,   -- user whose access is elevated
    initiator_id    uuid        NOT NULL,   -- user who requested the break-glass
    scope           jsonb       NOT NULL,   -- {"policy_ids":["uuid",...]} or {"all_opted_in":true}
    reason          text        NOT NULL CHECK (char_length(reason) BETWEEN 10 AND 2000),
    approvers       uuid[]      NOT NULL DEFAULT '{}',
    status          text        NOT NULL DEFAULT 'pending_approval'
                    CHECK (status IN ('pending_approval','active','terminated','expired')),
    max_duration_sec integer    NOT NULL DEFAULT 3600 CHECK (max_duration_sec BETWEEN 60 AND 3600),
    started_at      timestamptz,
    expires_at      timestamptz,
    terminated_at   timestamptz,
    terminated_by   uuid,
    summary         jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    -- Approver must not be the initiator (enforced in application layer + here).
    CONSTRAINT initiator_cannot_approve CHECK (
        NOT (initiator_id = ANY(approvers))
    )
);

ALTER TABLE breakglass_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE breakglass_sessions FORCE ROW LEVEL SECURITY;

CREATE POLICY breakglass_sessions_tenant_isolation ON breakglass_sessions
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_breakglass_sessions_tenant_pending
    ON breakglass_sessions (tenant_id, status)
    WHERE status IN ('pending_approval', 'active');

CREATE INDEX IF NOT EXISTS idx_breakglass_sessions_expires
    ON breakglass_sessions (expires_at)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_breakglass_sessions_principal
    ON breakglass_sessions (tenant_id, principal_id, status);

-- ── breakglass_audit ───────────────────────────────────────────────────────────
-- Immutable, hash-chained audit record of every break-glass action.
-- Separate chain from the main policy_audit for containment.
CREATE TABLE IF NOT EXISTS breakglass_audit (
    id              uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id       uuid        NOT NULL,
    session_id      uuid        NOT NULL REFERENCES breakglass_sessions(id),
    actor_id        uuid        NOT NULL,
    action          text        NOT NULL,
    resource_type   text,
    resource_id     text,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    hash            text,
    prev_hash       text,
    seq             bigint      NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE breakglass_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE breakglass_audit FORCE ROW LEVEL SECURITY;

CREATE POLICY breakglass_audit_tenant_isolation ON breakglass_audit
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_breakglass_audit_session_at
    ON breakglass_audit (session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_breakglass_audit_tenant_at
    ON breakglass_audit (tenant_id, created_at DESC);

-- ── step_up_obligations ────────────────────────────────────────────────────────
-- Tracks pending MFA step-up obligations per user session.
CREATE TABLE IF NOT EXISTS step_up_obligations (
    id               uuid        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id        uuid        NOT NULL,
    user_id          uuid        NOT NULL,
    session_jti      text        NOT NULL,
    obligation_token text        NOT NULL,
    reason           text        NOT NULL DEFAULT 'risk_threshold',
    risk_score       smallint,
    satisfied_at     timestamptz,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE step_up_obligations ENABLE ROW LEVEL SECURITY;
ALTER TABLE step_up_obligations FORCE ROW LEVEL SECURITY;

CREATE POLICY step_up_obligations_tenant_isolation ON step_up_obligations
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE INDEX IF NOT EXISTS idx_step_up_obligations_user_pending
    ON step_up_obligations (tenant_id, user_id, expires_at)
    WHERE satisfied_at IS NULL;

-- ── auto_response_log ──────────────────────────────────────────────────────────
-- Idempotent record of every auto-response action. Deduplication by window_key.
CREATE TABLE IF NOT EXISTS auto_response_log (
    id               uuid        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL,
    user_id          uuid        NOT NULL,
    action           text        NOT NULL CHECK (action IN ('normal','tag','step_up','mask','block')),
    risk_score       smallint    NOT NULL,
    tier             text        NOT NULL,
    playbook_id      uuid,
    playbook_version integer,
    window_key       text        NOT NULL,
    metadata         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at       timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE IF NOT EXISTS auto_response_log_default
    PARTITION OF auto_response_log DEFAULT;

ALTER TABLE auto_response_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE auto_response_log FORCE ROW LEVEL SECURITY;

CREATE POLICY auto_response_log_tenant_isolation ON auto_response_log
    USING (tenant_id = current_setting('app.current_tenant_id', true)::uuid);

CREATE UNIQUE INDEX IF NOT EXISTS idx_auto_response_log_idempotency
    ON auto_response_log (window_key);

CREATE INDEX IF NOT EXISTS idx_auto_response_log_tenant_user_at
    ON auto_response_log (tenant_id, user_id, created_at DESC);

-- ── RBAC grants ────────────────────────────────────────────────────────────────
GRANT SELECT, INSERT, UPDATE ON risk_playbooks        TO app_user;
GRANT SELECT, INSERT, UPDATE ON breakglass_sessions   TO app_user;
GRANT SELECT, INSERT         ON breakglass_audit      TO app_user;
GRANT SELECT, INSERT, UPDATE ON step_up_obligations   TO app_user;
GRANT SELECT, INSERT         ON auto_response_log     TO app_user;
