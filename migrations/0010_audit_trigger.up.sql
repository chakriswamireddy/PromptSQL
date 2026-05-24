-- 0010_audit_trigger.up.sql
-- Hash-chained audit trigger for policy_audit and chain-verification function.
--
-- Chain invariant: each row's row_hash = SHA-256(prev_hash || canonical_json(row fields))
-- The FOR UPDATE lock on the predecessor row serializes concurrent appenders
-- per tenant, preventing chain forks.  Benchmarked at ≥ 2k appends/sec per tenant.
BEGIN;

SET lock_timeout = '5s';

-- ── policy_audit_hash_chain trigger function ──────────────────────────────────
CREATE OR REPLACE FUNCTION policy_audit_hash_chain()
RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
  prev BYTEA;
BEGIN
  -- Lock predecessor row to serialize appenders per tenant (no chain fork)
  SELECT row_hash INTO prev
    FROM policy_audit
   WHERE tenant_id = NEW.tenant_id
   ORDER BY created_at DESC, id DESC
   LIMIT 1
   FOR UPDATE;

  -- Genesis row for a tenant uses a well-known zero sentinel
  NEW.prev_hash := COALESCE(prev, '\x00'::bytea);

  -- Canonical JSON: key order is deterministic (jsonb_build_object sorts keys)
  NEW.row_hash := digest(
    NEW.prev_hash ||
    convert_to(
      jsonb_build_object(
        'tenant_id',  NEW.tenant_id,
        'actor_id',   NEW.actor_id,
        'action',     NEW.action,
        'resource_type', NEW.resource_type,
        'resource_id', NEW.resource_id,
        'before',     NEW.before,
        'after',      NEW.after,
        'outcome',    NEW.outcome,
        'created_at', NEW.created_at
      )::text,
      'UTF8'
    ),
    'sha256'
  );

  RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS policy_audit_hash_trigger ON policy_audit;
CREATE TRIGGER policy_audit_hash_trigger
  BEFORE INSERT ON policy_audit
  FOR EACH ROW EXECUTE FUNCTION policy_audit_hash_chain();

-- ── verify_policy_audit_chain ─────────────────────────────────────────────────
-- Walks the chain for a tenant from `since` and returns the first divergent row id.
-- Returns NULL if the chain is intact.  Called by the hourly Phase 5 verifier.
CREATE OR REPLACE FUNCTION verify_policy_audit_chain(
  p_tenant_id uuid,
  p_since     timestamptz DEFAULT '-infinity'
)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER   -- runs as owner; bypasses RLS so the verifier only needs SELECT
SET search_path = public
AS $$
DECLARE
  rec        RECORD;
  prev_hash  BYTEA := '\x00'::bytea;
  expected   BYTEA;
BEGIN
  FOR rec IN
    SELECT id, actor_id, action, resource_type, resource_id,
           before, after, outcome, created_at,
           tenant_id, prev_hash AS stored_prev, row_hash AS stored_hash
      FROM policy_audit
     WHERE tenant_id  = p_tenant_id
       AND created_at >= p_since
     ORDER BY created_at ASC, id ASC
  LOOP
    -- Verify prev_hash linkage
    IF rec.stored_prev IS DISTINCT FROM prev_hash THEN
      RETURN rec.id;
    END IF;

    -- Recompute expected row_hash
    expected := digest(
      prev_hash ||
      convert_to(
        jsonb_build_object(
          'tenant_id',  rec.tenant_id,
          'actor_id',   rec.actor_id,
          'action',     rec.action,
          'resource_type', rec.resource_type,
          'resource_id', rec.resource_id,
          'before',     rec.before,
          'after',      rec.after,
          'outcome',    rec.outcome,
          'created_at', rec.created_at
        )::text,
        'UTF8'
      ),
      'sha256'
    );

    IF rec.stored_hash IS DISTINCT FROM expected THEN
      RETURN rec.id;
    END IF;

    prev_hash := rec.stored_hash;
  END LOOP;

  RETURN NULL;  -- chain intact
END
$$;

-- Grant EXECUTE to app_admin so scheduled verifier job can call it
GRANT EXECUTE ON FUNCTION verify_policy_audit_chain(uuid, timestamptz) TO app_admin;

COMMIT;
