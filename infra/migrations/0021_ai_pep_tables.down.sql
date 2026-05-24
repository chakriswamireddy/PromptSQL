-- Phase 10 rollback — remove PEP tables.
-- Dev/staging only; forward-only in production.

BEGIN;

DROP TABLE IF EXISTS pep_result_cache CASCADE;
DROP TABLE IF EXISTS pep_evals CASCADE;
DROP TABLE IF EXISTS saved_questions CASCADE;
DROP TABLE IF EXISTS ai_pep_sessions CASCADE;

-- Revert graph_type column from ai_token_budgets
ALTER TABLE ai_token_budgets DROP COLUMN IF EXISTS graph_type;

COMMIT;
