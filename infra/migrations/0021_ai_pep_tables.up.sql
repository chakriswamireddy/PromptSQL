-- Phase 10: AI PEP Graph — NL → Safe SQL
-- Tables: ai_pep_sessions, saved_questions, pep_evals
-- All tenant-scoped with RLS FORCE.

BEGIN;

-- ─── ai_pep_sessions ────────────────────────────────────────────────────────
-- One row per NL→SQL conversation turn.  Stores the resolved snapshot hash,
-- the validated AST, the final SQL, execution stats and cost.

CREATE TABLE IF NOT EXISTS ai_pep_sessions (
    id                  UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id           UUID        NOT NULL,
    user_id             UUID        NOT NULL,
    data_source_id      UUID        NOT NULL,
    idempotency_key     TEXT        NOT NULL,
    prompt              TEXT        NOT NULL CHECK (length(prompt) <= 8192),
    prompt_hash         TEXT        NOT NULL,
    snapshot_hash       TEXT,
    ast_json            JSONB,
    sql_text            TEXT,
    -- execution outcome
    status              TEXT        NOT NULL DEFAULT 'running'
                            CHECK (status IN ('running','done','error','cancelled')),
    rows_returned       INTEGER,
    cost_usd            NUMERIC(10, 6)   DEFAULT 0,
    tokens_in           INTEGER          DEFAULT 0,
    tokens_out          INTEGER          DEFAULT 0,
    model_metadata      JSONB,
    -- validation / rejection
    validation_errors   JSONB,
    rejection_reason    TEXT,
    -- timing
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at            TIMESTAMPTZ,
    -- user feedback
    thumbs_up           BOOLEAN,
    feedback_comment    TEXT,

    CONSTRAINT ai_pep_sessions_idempotency UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS ai_pep_sessions_tenant_user
    ON ai_pep_sessions (tenant_id, user_id, started_at DESC);

CREATE INDEX IF NOT EXISTS ai_pep_sessions_prompt_hash
    ON ai_pep_sessions (tenant_id, prompt_hash);

ALTER TABLE ai_pep_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_pep_sessions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_pep_sessions_tenant ON ai_pep_sessions;
CREATE POLICY ai_pep_sessions_tenant ON ai_pep_sessions
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ─── saved_questions ─────────────────────────────────────────────────────────
-- Users save a NL question + its validated SQL for repeat use.
-- Re-running skips LLM if snapshot_hash + policy_set_version match.

CREATE TABLE IF NOT EXISTS saved_questions (
    id                  UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id           UUID        NOT NULL,
    user_id             UUID        NOT NULL,
    data_source_id      UUID        NOT NULL,
    name                TEXT        NOT NULL CHECK (length(name) <= 255),
    description         TEXT        CHECK (length(description) <= 2048),
    prompt              TEXT        NOT NULL,
    sql_text            TEXT        NOT NULL,
    snapshot_hash       TEXT        NOT NULL,
    policy_set_version  TEXT,
    -- publishing: published = visible to all tenant users
    is_published        BOOLEAN     NOT NULL DEFAULT FALSE,
    published_by        UUID,
    published_at        TIMESTAMPTZ,
    -- stats
    run_count           INTEGER     NOT NULL DEFAULT 0,
    last_run_at         TIMESTAMPTZ,
    last_session_id     UUID REFERENCES ai_pep_sessions (id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS saved_questions_tenant_user
    ON saved_questions (tenant_id, user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS saved_questions_tenant_published
    ON saved_questions (tenant_id, is_published) WHERE is_published = TRUE;

ALTER TABLE saved_questions ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_questions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS saved_questions_tenant ON saved_questions;
CREATE POLICY saved_questions_tenant ON saved_questions
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ─── pep_evals ───────────────────────────────────────────────────────────────
-- Golden set of (NL prompt, expected SQL pattern) pairs.
-- CI runs these nightly; regression blocks release.

CREATE TABLE IF NOT EXISTS pep_evals (
    id                  UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    tenant_id           UUID        NOT NULL,
    data_source_id      UUID        NOT NULL,
    prompt              TEXT        NOT NULL,
    prompt_hash         TEXT        NOT NULL,
    expected_sql_pattern TEXT,           -- regex or exact match
    expected_tables     TEXT[],
    expected_columns    TEXT[],
    adversarial         BOOLEAN     NOT NULL DEFAULT FALSE,  -- exfiltration / injection
    last_actual_sql     TEXT,
    last_outcome        TEXT        CHECK (last_outcome IN ('pass','fail','skip')),
    last_run_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pep_evals_prompt_unique UNIQUE (tenant_id, prompt_hash)
);

ALTER TABLE pep_evals ENABLE ROW LEVEL SECURITY;
ALTER TABLE pep_evals FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pep_evals_tenant ON pep_evals;
CREATE POLICY pep_evals_tenant ON pep_evals
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ─── result cache table ──────────────────────────────────────────────────────
-- Short-lived (30 s) keyed cache for identical (user, querySha, policySetVersion).
-- TTL enforced by application; this table is for cross-replica coherence.

CREATE TABLE IF NOT EXISTS pep_result_cache (
    cache_key           TEXT        NOT NULL PRIMARY KEY,
    tenant_id           UUID        NOT NULL,
    result_json         JSONB       NOT NULL,
    row_count           INTEGER     NOT NULL DEFAULT 0,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS pep_result_cache_expires
    ON pep_result_cache (expires_at);

ALTER TABLE pep_result_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE pep_result_cache FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS pep_result_cache_tenant ON pep_result_cache;
CREATE POLICY pep_result_cache_tenant ON pep_result_cache
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid);

-- ─── Extend ai_token_budgets to cover PEP ────────────────────────────────────
-- Phase 9 created this table; add graph_type discriminator.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'ai_token_budgets' AND column_name = 'graph_type'
    ) THEN
        ALTER TABLE ai_token_budgets
            ADD COLUMN graph_type TEXT NOT NULL DEFAULT 'pap'
                CHECK (graph_type IN ('pap','pep'));
    END IF;
END;
$$;

COMMIT;
