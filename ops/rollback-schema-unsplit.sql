-- ops/rollback-schema-unsplit.sql
-- Idempotent rollback: reverse the schema-split from cutover day 00:20.
-- Running this restores the monolith role's ability to read/write every
-- table and extension, regardless of whether the per-service GRANTs were
-- applied or not. Safe to run against a partially-split DB.
--
-- Preconditions:
--   - Monolith Postgres role name: `lakehouse2ontology` (matches main.go
--     default DSN user).
--   - Per-service roles (if created by ops/db-roles.sql) are:
--     backend_api_user, agent_server_user, recall_server_user,
--     lakehouse_sql_server_user, mcp_tools_server_user.
--
-- Execution: psql $DATABASE_URL -f ops/rollback-schema-unsplit.sql
-- Expected wall time: < 5 seconds on a healthy DB (metadata operations
-- only; no row scans).

BEGIN;

-- 1. Re-GRANT monolith role full access on every table. Idempotent —
--    GRANT ON ALL TABLES is safe to re-run. Schema `public` covers
--    every table on this branch.
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO lakehouse2ontology;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO lakehouse2ontology;
GRANT USAGE, CREATE ON SCHEMA public TO lakehouse2ontology;

-- 2. REVOKE per-service role privileges if those roles exist. Using
--    DO blocks so the script doesn't fail on missing roles (partial
--    split state).
DO $$
DECLARE r text;
BEGIN
  FOR r IN SELECT unnest(ARRAY[
    'backend_api_user', 'agent_server_user', 'recall_server_user',
    'lakehouse_sql_server_user', 'mcp_tools_server_user'
  ]) LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %I', r);
      EXECUTE format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %I', r);
      EXECUTE format('REVOKE USAGE ON SCHEMA public FROM %I', r);
    END IF;
  END LOOP;
END $$;

-- 3. Verify monolith role can UPDATE ont_agent_thread (the table with
--    strictest access in the split topology). Fails loudly if missed.
DO $$
BEGIN
  IF NOT has_table_privilege('lakehouse2ontology', 'ont_agent_thread', 'UPDATE') THEN
    RAISE EXCEPTION 'Rollback failed: lakehouse2ontology role cannot UPDATE ont_agent_thread';
  END IF;
  IF NOT has_table_privilege('lakehouse2ontology', 'ont_agent_step', 'INSERT') THEN
    RAISE EXCEPTION 'Rollback failed: lakehouse2ontology role cannot INSERT ont_agent_step';
  END IF;
END $$;

-- 4. Ensure pgvector extension reachable (monolith needs it; harmless
--    if still installed).
CREATE EXTENSION IF NOT EXISTS vector;

-- 5. Touch a sentinel row so downstream health checks surface that
--    rollback ran. The table-less version uses pg_stat_user_tables.
COMMENT ON SCHEMA public IS
  'Rollback-unsplit executed at ' || now()::text;

COMMIT;

-- Verification after this script:
--   psql -c "SELECT has_table_privilege('lakehouse2ontology','ont_agent_thread','UPDATE');" → t
--   psql -c "SELECT has_table_privilege('backend_api_user','ont_agent_thread','UPDATE');"   → f (if role exists)
