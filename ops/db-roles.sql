-- ops/db-roles.sql
-- CREATE per-service Postgres roles + GRANT scoped access per service.
-- Idempotent: safe to re-run. Executed at cutover T-0 00:22 after schema
-- split DDL.
--
-- Ownership boundaries (matches §3.4):
--   backend_api_user            → user/project/ontology tables (RW), agent tables (RO, via /internal/ledger/get proxy only)
--   agent_server_user           → agent tables (RW), ontology read (RO), lakehouse read (RO)
--   recall_server_user          → ontology read (RO), lakehouse read (RO), vector write (RW on ont_vector_entry)
--   lakehouse_sql_server_user   → lakehouse/staging tables (RW), ontology read (RO)
--   mcp_tools_server_user       → no direct DB access; proxies through recall-server + lakehouse-sql-server

BEGIN;

-- 1. Create roles if missing (LOGIN so services can authenticate; no password set here).
--    RUNBOOK REQUIREMENT: immediately after running this script the operator MUST
--    set a strong random password for each role:
--
--      ALTER ROLE backend_api_user          PASSWORD '<from secrets manager>';
--      ALTER ROLE agent_server_user         PASSWORD '<from secrets manager>';
--      ALTER ROLE recall_server_user        PASSWORD '<from secrets manager>';
--      ALTER ROLE lakehouse_sql_server_user PASSWORD '<from secrets manager>';
--      ALTER ROLE mcp_tools_server_user     PASSWORD '<from secrets manager>';
--
--    Roles intentionally have NO PASSWORD on initial CREATE to prevent a
--    known-weak placeholder ('rotate_at_deploy') from ever being live.
--    The .env.shared file must supply matching credentials via DATABASE_URL.
DO $$
DECLARE r text;
BEGIN
  FOR r IN SELECT unnest(ARRAY[
    'backend_api_user', 'agent_server_user', 'recall_server_user',
    'lakehouse_sql_server_user', 'mcp_tools_server_user'
  ]) LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('CREATE ROLE %I LOGIN', r);  -- no password: operator must rotate immediately
    END IF;
  END LOOP;
END $$;

-- 2. GRANT schema-level USAGE so roles can see table names.
GRANT USAGE ON SCHEMA public TO backend_api_user, agent_server_user, recall_server_user,
    lakehouse_sql_server_user, mcp_tools_server_user;

-- 3. backend_api_user: RW on user/project/ontology; RO on agent/lakehouse.
-- NOTE (v2b REV-1 fix): ont_vector_entry removed from RW list. Post-split,
-- backend-api writes vectors only via HTTP to recall-server; direct DB write
-- is a defense-in-depth denial. SELECT retained for read-only endpoints like
-- /api/ontology/learned-facts that surface vector metadata.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE
    "user", project, project_member,
    prompt_config, llm_config,
    ont_version, ont_object_type, ont_property, ont_link_type,
    ont_knowledge, ont_causality, ont_learned_fact, ont_fact_link,
    lakehouse_keyword, lakehouse_metric_intent
  TO backend_api_user;
GRANT SELECT ON TABLE ont_agent_thread, ont_agent_step, ont_vector_entry TO backend_api_user;
-- Explicit REVOKE on UPDATE of thread_state to enforce P4.
REVOKE UPDATE ON TABLE ont_agent_thread FROM backend_api_user;
REVOKE INSERT, DELETE ON TABLE ont_agent_thread FROM backend_api_user;
-- Defense-in-depth: prevent accidental backend-api vector writes (v2b REV-1).
REVOKE INSERT, UPDATE, DELETE ON TABLE ont_vector_entry FROM backend_api_user;

-- 4. agent_server_user: RW on agent tables; RO on ontology/lakehouse.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE ont_agent_thread, ont_agent_step TO agent_server_user;
GRANT SELECT ON TABLE
    ont_version, ont_object_type, ont_property, ont_link_type,
    ont_knowledge, ont_causality, ont_learned_fact, ont_fact_link,
    ont_vector_entry, lakehouse_keyword, lakehouse_metric_intent,
    "user", project
  TO agent_server_user;

-- 5. recall_server_user: RO on ontology; RW on ont_vector_entry (for embeddings).
GRANT SELECT ON TABLE
    ont_version, ont_object_type, ont_property, ont_link_type,
    ont_knowledge, ont_causality, ont_learned_fact, ont_fact_link,
    lakehouse_keyword, lakehouse_metric_intent
  TO recall_server_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE ont_vector_entry TO recall_server_user;

-- 6. lakehouse_sql_server_user: RW on lakehouse/staging; RO on ontology.
-- Staging tables are dynamic (per-project); grant ALL on schema public
-- so new staging tables inherit access.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO lakehouse_sql_server_user;
-- But REVOKE write on agent + ontology tables (they're not staging).
-- NOTE (v2b REV-1 fix): lakehouse_keyword + lakehouse_metric_intent added to
-- REVOKE list. They are ontology-layer tables owned by backend-api per §3.4;
-- without this REVOKE, scripts/check-runtime-grants.sh line
-- `check lakehouse_sql_server_user lakehouse_keyword UPDATE f` fails.
REVOKE INSERT, UPDATE, DELETE ON TABLE
    ont_agent_thread, ont_agent_step,
    "user", project, project_member, prompt_config, llm_config,
    ont_version, ont_object_type, ont_property, ont_link_type,
    ont_knowledge, ont_causality, ont_learned_fact, ont_fact_link,
    ont_vector_entry,
    lakehouse_keyword, lakehouse_metric_intent
  FROM lakehouse_sql_server_user;

-- 7. Sequence grants for INSERTing roles.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO
    backend_api_user, agent_server_user, lakehouse_sql_server_user;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO recall_server_user;

COMMIT;

-- Verification (optional manual; also run by scripts/check-runtime-grants.sh):
--   SELECT has_table_privilege('backend_api_user', 'ont_agent_thread', 'UPDATE'); → f
--   SELECT has_table_privilege('agent_server_user', 'ont_agent_thread', 'UPDATE'); → t
--   SELECT has_table_privilege('recall_server_user', 'ont_agent_thread', 'SELECT'); → f
