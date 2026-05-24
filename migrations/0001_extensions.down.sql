-- 0001_extensions.down.sql  (dev/staging only)
BEGIN;
DROP EXTENSION IF EXISTS vector;
DROP EXTENSION IF EXISTS citext;
DROP EXTENSION IF EXISTS pgcrypto;
COMMIT;
