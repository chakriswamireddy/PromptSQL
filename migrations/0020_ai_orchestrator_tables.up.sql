-- Phase 9: AI PAP Graph tables
-- ai_sessions tracks every graph run; ai_evals holds the CI golden set;
-- ai_token_budgets enforces per-tenant rolling usage caps.

-- ─── ai_sessions ─────────────────────────────────────────────────────────────
CREATE TABLE ai_sessions (
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    user_id         UUID        NOT NULL,
    idempotency_key TEXT        NOT NULL,  -- (authorId||draftHash||promptHash)
    prompt          TEXT        NOT NULL,
    prompt_hash     TEXT        NOT NULL,  -- SHA-256 hex
    graph_run       JSONB       NOT NULL DEFAULT '{}',
    status          TEXT        NOT NULL DEFAULT 'running'
                                CHECK (status IN ('running','draft','approved','rejected','error')),
    cost_usd        NUMERIC(10,6) NOT NULL DEFAULT 0,
    tokens_in       INTEGER     NOT NULL DEFAULT 0,
    tokens_out      INTEGER     NOT NULL DEFAULT 0,
    model_metadata  JSONB       NOT NULL DEFAULT '{}',
    draft_policy_id UUID,
    error_message   TEXT,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at        TIMESTAMPTZ,
    CONSTRAINT ai_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT ai_sessions_idempotency_key UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX ai_sessions_tenant_user_idx ON ai_sessions (tenant_id, user_id, started_at DESC);
CREATE INDEX ai_sessions_status_idx      ON ai_sessions (tenant_id, status);

ALTER TABLE ai_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_sessions FORCE ROW LEVEL SECURITY;

CREATE POLICY ai_sessions_tenant_isolation ON ai_sessions
    USING (tenant_id = current_setting('app.tenant_id', true)::UUID);

-- hash-chain trigger (reuses same pattern as policy_audit)
CREATE OR REPLACE FUNCTION ai_session_set_row_hash()
RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
    prev_hash TEXT := '';
BEGIN
    SELECT row_hash INTO prev_hash
      FROM ai_sessions
     WHERE tenant_id = NEW.tenant_id
       AND ended_at IS NOT NULL
     ORDER BY ended_at DESC
     LIMIT 1;
    NEW.model_metadata = jsonb_set(
        COALESCE(NEW.model_metadata, '{}'::jsonb),
        '{row_hash}',
        to_jsonb(encode(sha256((prev_hash || NEW.id::text || NEW.prompt_hash)::bytea), 'hex'))
    );
    RETURN NEW;
END;
$$;

CREATE TRIGGER ai_session_hash_chain
    BEFORE UPDATE OF ended_at ON ai_sessions
    FOR EACH ROW EXECUTE FUNCTION ai_session_set_row_hash();

-- ─── ai_evals ────────────────────────────────────────────────────────────────
-- CI golden-set: 50 NL prompts + expected canonical Policy JSON. Gate: ≥ 90%.
CREATE TABLE ai_evals (
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    prompt_hash     TEXT        NOT NULL,
    prompt          TEXT        NOT NULL,
    expected_policy JSONB       NOT NULL,
    last_actual     JSONB,
    last_outcome    TEXT        CHECK (last_outcome IN ('pass','fail',NULL)),
    last_run_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ai_evals_pkey PRIMARY KEY (id),
    CONSTRAINT ai_evals_prompt_hash_unique UNIQUE (tenant_id, prompt_hash)
);

ALTER TABLE ai_evals ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_evals FORCE ROW LEVEL SECURITY;

CREATE POLICY ai_evals_tenant_isolation ON ai_evals
    USING (tenant_id = current_setting('app.tenant_id', true)::UUID);

-- ─── ai_token_budgets ────────────────────────────────────────────────────────
-- Rolling token + cost caps enforced before graph dispatch.
CREATE TABLE ai_token_budgets (
    id                  UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    budget_period       TEXT        NOT NULL CHECK (budget_period IN ('minute','day')),
    period_start        TIMESTAMPTZ NOT NULL,
    tokens_used         BIGINT      NOT NULL DEFAULT 0,
    cost_used_usd       NUMERIC(10,4) NOT NULL DEFAULT 0,
    tokens_limit        BIGINT      NOT NULL DEFAULT 100000,   -- 100k/day
    cost_limit_usd      NUMERIC(10,4) NOT NULL DEFAULT 10.00,  -- $10/day
    CONSTRAINT ai_token_budgets_pkey PRIMARY KEY (id),
    CONSTRAINT ai_token_budgets_period_unique UNIQUE (tenant_id, budget_period, period_start)
);

ALTER TABLE ai_token_budgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_token_budgets FORCE ROW LEVEL SECURITY;

CREATE POLICY ai_token_budgets_tenant_isolation ON ai_token_budgets
    USING (tenant_id = current_setting('app.tenant_id', true)::UUID);

-- ─── policies columns for AI provenance ──────────────────────────────────────
ALTER TABLE policies
    ADD COLUMN IF NOT EXISTS created_by_ai    BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS ai_session_id    UUID        REFERENCES ai_sessions(id);

CREATE INDEX policies_ai_session_idx ON policies (ai_session_id) WHERE ai_session_id IS NOT NULL;

-- ─── scoped role grants ───────────────────────────────────────────────────────
GRANT SELECT, INSERT, UPDATE ON ai_sessions       TO app_readwrite;
GRANT SELECT, INSERT, UPDATE ON ai_evals          TO app_readwrite;
GRANT SELECT, INSERT, UPDATE ON ai_token_budgets  TO app_readwrite;
GRANT SELECT ON ai_sessions      TO app_readonly;
GRANT SELECT ON ai_evals         TO app_readonly;
GRANT SELECT ON ai_token_budgets TO app_readonly;
