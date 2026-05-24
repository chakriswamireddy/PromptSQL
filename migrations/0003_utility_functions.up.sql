-- 0003_utility_functions.up.sql
-- Shared utility functions used across all tenant-scoped tables.
BEGIN;

SET lock_timeout = '5s';

-- gen_uuidv7(): time-ordered UUID v7 for index locality.
-- Layout: [48-bit Unix ms][4-bit ver=7][12-bit rand][2-bit var=10][62-bit rand]
CREATE OR REPLACE FUNCTION gen_uuidv7()
RETURNS uuid
LANGUAGE plpgsql
VOLATILE PARALLEL SAFE
AS $$
DECLARE
  ts_ms BIGINT := (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT;
  bytes BYTEA  := gen_random_bytes(16);
BEGIN
  -- Overwrite bytes 0-5 with 48-bit Unix ms timestamp (big-endian)
  bytes := set_byte(bytes, 0, (ts_ms >> 40) & 255);
  bytes := set_byte(bytes, 1, (ts_ms >> 32) & 255);
  bytes := set_byte(bytes, 2, (ts_ms >> 24) & 255);
  bytes := set_byte(bytes, 3, (ts_ms >> 16) & 255);
  bytes := set_byte(bytes, 4, (ts_ms >>  8) & 255);
  bytes := set_byte(bytes, 5,  ts_ms        & 255);
  -- Byte 6: high nibble = version 7 (0111 xxxx)
  bytes := set_byte(bytes, 6, (get_byte(bytes, 6) & 15) | 112);
  -- Byte 8: variant = 10xx xxxx
  bytes := set_byte(bytes, 8, (get_byte(bytes, 8) & 63) | 128);
  RETURN (
    substr(encode(bytes,'hex'),  1, 8) || '-' ||
    substr(encode(bytes,'hex'),  9, 4) || '-' ||
    substr(encode(bytes,'hex'), 13, 4) || '-' ||
    substr(encode(bytes,'hex'), 17, 4) || '-' ||
    substr(encode(bytes,'hex'), 21, 12)
  )::uuid;
END
$$;

-- set_updated_at(): trigger function to keep updated_at current.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END
$$;

-- assert_tenant_id_match(): trigger function that rejects INSERT with
-- tenant_id != current_setting('app.tenant_id').  Applied to audit tables
-- as a defence-in-depth check behind RLS.
CREATE OR REPLACE FUNCTION assert_tenant_id_match()
RETURNS TRIGGER
LANGUAGE plpgsql AS $$
DECLARE
  ctx_tenant uuid;
BEGIN
  ctx_tenant := current_setting('app.tenant_id', true)::uuid;
  IF ctx_tenant IS NOT NULL AND NEW.tenant_id != ctx_tenant THEN
    RAISE EXCEPTION 'tenant_id mismatch: row=% context=%', NEW.tenant_id, ctx_tenant
      USING ERRCODE = 'RLS04';
  END IF;
  RETURN NEW;
END
$$;

COMMIT;
