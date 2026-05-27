-- ops/db-roles.sql
-- CREATE per-service Postgres roles + GRANT scoped access per service.
-- Idempotent: safe to re-run. Executed at cutover T-0 00:22 after schema
-- split DDL.
--
-- Ownership boundaries (matches §3.4):
--   backend_api_user            → user/project/ontology tables (RW), agent tables (RO, via /internal/ledger/get proxy only)
--   agent_server_user           → agent tables (RW), ontology read (RO), lakehouse read (RO)
--   recall_server_user          → ontology read (RO), lakehouse read (RO), vector columns read-only (vectors written by collector)
--   lakehouse_sql_server_user   → lakehouse/staging tables (RW), ontology read (RO)
--   mcp_tools_server_user       → no direct DB access; proxies through recall-server + lakehouse-sql-server
--   collector_server_user       → heavy ingestion writer: RW on ingestion/ontology-population
--                                 tables, broad RO, plus CREATE on DB for runtime
--                                 per-project schema creation (CREATE SCHEMA proj_<hex>)

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
--      ALTER ROLE collector_server_user     PASSWORD '<from secrets manager>';
--
--    Roles intentionally have NO PASSWORD on initial CREATE to prevent a
--    known-weak placeholder ('rotate_at_deploy') from ever being live.
--    The .env.shared file must supply matching credentials via DATABASE_URL.
DO $$
DECLARE r text;
BEGIN
  FOR r IN SELECT unnest(ARRAY[
    'backend_api_user', 'agent_server_user', 'recall_server_user',
    'lakehouse_sql_server_user', 'mcp_tools_server_user',
    'collector_server_user'
  ]) LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('CREATE ROLE %I LOGIN', r);  -- no password: operator must rotate immediately
    END IF;
  END LOOP;
END $$;

-- 2. GRANT schema-level USAGE so roles can see table names.
GRANT USAGE ON SCHEMA public TO backend_api_user, agent_server_user, recall_server_user,
    lakehouse_sql_server_user, mcp_tools_server_user, collector_server_user;

-- Helper: apply a GRANT/REVOKE verb only to tables that actually exist, so the
-- script is idempotent + applyable across schema variants. Some named tables
-- below (e.g. ont_version, ont_vector_entry) are absent in the -enterprise clone
-- schema; without this tolerance a single missing relation aborts the whole
-- transaction. Privilege intent is unchanged — only absent tables are skipped
-- (with a NOTICE). Added for Wave-4 production-readiness apply.
CREATE OR REPLACE FUNCTION pg_temp.grant_if_exists(
  verb text, privs text, tables text[], role text
) RETURNS void AS $fn$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY tables LOOP
    IF to_regclass('public.' || quote_ident(t)) IS NOT NULL THEN
      EXECUTE format('%s %s ON TABLE public.%I %s %I',
        verb, privs, t, CASE WHEN verb = 'GRANT' THEN 'TO' ELSE 'FROM' END, role);
    ELSE
      RAISE NOTICE '%(%) on public.% skipped — relation absent', verb, role, t;
    END IF;
  END LOOP;
END $fn$ LANGUAGE plpgsql;

-- 3. backend_api_user: RW on user/project/ontology; RO on agent/lakehouse.
-- NOTE (v2b REV-1 fix): ont_vector_entry removed from RW list. Post-split,
-- backend-api writes vectors only via HTTP to recall-server; direct DB write
-- is a defense-in-depth denial. SELECT retained for read-only endpoints like
-- /api/ontology/learned-facts that surface vector metadata.
-- NOTE (grant-gap fix): ont_alias + ont_query_log added — handler_alias.go and
-- handler_query_log.go do full CRUD (list/create/mark/delete aliases + the
-- question-history UI). ont_lakehouse_sql_log + ont_sql_passthrough_log added —
-- backend-api inserts/deletes/reads the SQL execution log views. ont_agent_annotation
-- added read-only (handler reads token-annotation rows; agent-server owns writes).
-- ont_fact_definition added — the learned-facts list subquery counts it. ont_topic +
-- ont_knowledge_definition + ont_knowledge_example added — the knowledge list (which the
-- ontology graph needs to map join_key causality edges onto property nodes) LEFT JOINs
-- ont_topic and subqueries the def/example counts.
-- Without these the corresponding public endpoints silently return empty (permission
-- denied swallowed into an empty result set).
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, INSERT, UPDATE, DELETE', ARRAY[
    'user', 'project', 'project_member',
    'prompt_config', 'llm_config', 'llm_role_binding',
    'ont_version', 'ont_object_type', 'ont_property', 'ont_link_type', 'ont_alias',
    'ont_knowledge', 'ont_topic', 'ont_knowledge_definition', 'ont_knowledge_example',
    'ont_causality', 'ont_learned_fact', 'ont_fact_definition', 'ont_fact_link',
    'lakehouse_keyword', 'lakehouse_keyword_alias_vector', 'lakehouse_metric_intent',
    'lakehouse_metric',
    'ont_query_log',
    'ont_lakehouse_sql_log', 'ont_sql_passthrough_log'
  ], 'backend_api_user');
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT',
  ARRAY['ont_agent_thread', 'ont_agent_step', 'ont_vector_entry', 'ont_agent_annotation'], 'backend_api_user');
-- app_setting: admin settings upsert (allow_registration) via PUT /api/admin/settings.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, INSERT, UPDATE', ARRAY['app_setting'], 'backend_api_user');
-- ingest_job: GET /api/jobs lists status + cancel sets cancel_requested (UPDATE); rows
-- are created/written by collector_server_user, so backend-api only needs SELECT + UPDATE.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, UPDATE', ARRAY['ingest_job'], 'backend_api_user');
-- Explicit REVOKE on UPDATE of thread_state to enforce P4.
SELECT pg_temp.grant_if_exists('REVOKE', 'UPDATE', ARRAY['ont_agent_thread'], 'backend_api_user');
SELECT pg_temp.grant_if_exists('REVOKE', 'INSERT, DELETE', ARRAY['ont_agent_thread'], 'backend_api_user');
-- Defense-in-depth: prevent accidental backend-api vector writes (v2b REV-1).
SELECT pg_temp.grant_if_exists('REVOKE', 'INSERT, UPDATE, DELETE', ARRAY['ont_vector_entry'], 'backend_api_user');

-- 4. agent_server_user: RW on agent tables; RO on ontology/lakehouse.
-- The LH-testing subsystem (suites/cases/runs) runs IN-PROCESS in agent-server
-- (StartLHTestWorkerCtx polls ont_test_run for queued work + the public
-- /api/ontology/lh-test-* handlers do full CRUD on all four tables), so
-- agent_server_user owns them outright. Missed in the original grant set;
-- without these the worker error-loops with "permission denied for table
-- ont_test_run". Wave 4-4 P1-8 cutover grant-gap fix.
-- NOTE (grant-gap fix): ont_agent_annotation + ont_test_case_tag +
-- ont_test_case_tag_link added — agent-server does full CRUD on the token-annotation
-- store and the test-case tag/tag-link tables; they were missed in the original set.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, INSERT, UPDATE, DELETE',
  ARRAY['ont_agent_thread', 'ont_agent_step', 'ont_agent_annotation',
        'ont_test_suite', 'ont_test_case', 'ont_test_run', 'ont_test_run_case',
        'ont_test_case_tag', 'ont_test_case_tag_link'],
  'agent_server_user');
-- NOTE (grant-gap fix): llm_config + llm_role_binding added — agent-server
-- resolves the agent/synthesizer/tokenize LLM model per role via
-- llmclient.GetConfigForRole (SELECT base_url/api_key/model from these tables).
-- Without SELECT the lookup returns an empty base_url, so the LLM POST hits a
-- bare "/v1/chat/completions" → "unsupported protocol scheme """.
-- NOTE (grant-gap fix): lakehouse_keyword_alias_vector added — agent-server runs
-- recall.LakehouseVectorTopN (token-recall debug page + annotations) which JOINs
-- this table for the alias-vector cosine UNION. Without SELECT the query errors
-- ("permission denied for table lakehouse_keyword_alias_vector"), LakehouseVectorTopN
-- returns nil, and the debug page shows "向量候选不可用" even though embeddings work.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT', ARRAY[
    'ont_version', 'ont_object_type', 'ont_property', 'ont_link_type',
    'ont_knowledge', 'ont_causality', 'ont_learned_fact', 'ont_fact_link',
    'lakehouse_keyword', 'lakehouse_keyword_alias_vector',
    'lakehouse_metric_intent', 'lakehouse_metric',
    'lakehouse_shape_capability',
    'llm_config', 'llm_role_binding',
    'user', 'project'
  ], 'agent_server_user');
-- NOTE (grant-gap fix): MissionAct tables. agent-server is the SOLE owner of
-- ont_mission — pkg/mission.Store upserts it (INSERT … ON CONFLICT DO UPDATE)
-- and SELECTs by id/thread; the thread SELECT also backs
-- GET /api/ontology/lakehouse-missions (HandleMissionsByThread), which returned
-- 500 "permission denied for table ont_mission" before this grant. Mission
-- writes are best-effort (logged, never fail the turn), so the gap stayed
-- invisible until the read endpoint surfaced it. capability_gap_log is an
-- INSERT-only audit sink (writeCapabilityGapLog). lakehouse_shape_capability
-- (added to the SELECT array above) is the reachability judge's shape vocab —
-- without SELECT loadShapeVocab silently returns nil and the gate degrades to
-- LLM-only, skipping the deterministic shape guard. ont_mission +
-- capability_gap_log use uuid text ids (no sequence). All under USE_MISSION_ACT.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, INSERT, UPDATE', ARRAY['ont_mission'], 'agent_server_user');
SELECT pg_temp.grant_if_exists('GRANT', 'INSERT', ARRAY['capability_gap_log'], 'agent_server_user');

-- 5. recall_server_user: RO on ontology + lakehouse (reads vector columns for cosine
-- similarity search). Vector *writes* (keyword_vector, alias_vector, content_vector
-- etc.) are done by collector_server_user during ontology population — recall-server
-- never writes. The old ont_vector_entry table no longer exists (refactored to
-- pgvector columns on existing tables in the enterprise schema); its grant is removed.
-- NOTE (grant-gap fix): llm_config + llm_role_binding added — recall-server calls
-- llmclient.EmbedTexts (Tier-3 VEC keyword search + LakehouseVectorTopN), which reads
-- the active embedding config (base_url/api_key/model) from these tables via
-- GetConfigForRoleWithProxy(db,"embedding"). Without SELECT the lookup returns an empty
-- config → "no active embedding config" → the whole VEC tier silently goes dark.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT', ARRAY[
    'ont_version', 'ont_object_type', 'ont_property', 'ont_link_type',
    'ont_knowledge', 'ont_causality', 'ont_learned_fact', 'ont_fact_link',
    'lakehouse_keyword', 'lakehouse_metric_intent', 'lakehouse_metric',
    'lakehouse_keyword_alias_vector',
    'llm_config', 'llm_role_binding'
  ], 'recall_server_user');

-- 6. lakehouse_sql_server_user: RW on lakehouse/staging; RO on ontology.
-- Staging tables are dynamic (per-project); grant ALL on schema public
-- so new staging tables inherit access.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO lakehouse_sql_server_user;
-- But REVOKE write on agent + ontology tables (they're not staging).
-- NOTE (v2b REV-1 fix): lakehouse_keyword + lakehouse_metric_intent added to
-- REVOKE list. They are ontology-layer tables owned by backend-api per §3.4;
-- without this REVOKE, scripts/check-runtime-grants.sh line
-- `check lakehouse_sql_server_user lakehouse_keyword UPDATE f` fails.
SELECT pg_temp.grant_if_exists('REVOKE', 'INSERT, UPDATE, DELETE', ARRAY[
    'ont_agent_thread', 'ont_agent_step',
    'user', 'project', 'project_member', 'prompt_config', 'llm_config',
    'ont_version', 'ont_object_type', 'ont_property', 'ont_link_type',
    'ont_knowledge', 'ont_causality', 'ont_learned_fact', 'ont_fact_link',
    'ont_vector_entry',
    'lakehouse_keyword', 'lakehouse_metric_intent', 'lakehouse_metric'
  ], 'lakehouse_sql_server_user');

-- 7. mcp_tools_server_user: the §3.4 comment says "no direct DB access", but the
-- shipped mcp-tools-server DOES touch one table: the `mcp_api_key` auth store.
-- auth/apikey.go does (a) a per-request SELECT to validate the bearer key +
-- best-effort UPDATE of last_used_at, and (b) an optional bootstrap INSERT when
-- MCP_API_KEY env is set. Without these grants the auth lookup fails ("permission
-- denied for table mcp_api_key") and every MCP call is rejected. Grant exactly
-- the apikey-store access — nothing else (it still proxies ontology/lakehouse/
-- recall reads over HTTP, never SQL). Wave 4-4 P1-8 cutover grant-gap fix.
SELECT pg_temp.grant_if_exists('GRANT', 'SELECT, INSERT, UPDATE', ARRAY['mcp_api_key'], 'mcp_tools_server_user');

-- 8. collector_server_user: heavy ingestion writer (Finding #3).
-- collector-server INSERT/UPDATE/DELETEs the ingestion + ontology-population
-- tables below, reads broadly across ontology/lakehouse, and creates per-project
-- schemas at runtime (CREATE SCHEMA proj_<hex>), so it needs CREATE on the DB.
-- Posture: least-privilege-but-functional — broad RO + targeted RW write set.
GRANT CREATE ON DATABASE "text2ontology_community" TO collector_server_user;  -- runtime CREATE SCHEMA proj_<hex>
GRANT USAGE ON SCHEMA public TO collector_server_user;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO collector_server_user;  -- reads broadly for ontology population
-- Write set: INSERT/UPDATE/DELETE on the audited collector-server write tables.
-- Tolerant of missing relations: if a table is absent in this clone, skip its
-- grant (and RAISE NOTICE) rather than aborting the whole transaction. Keeps the
-- script idempotent + safe to re-run across schema variants.
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'data_source', 'ingest_job', 'lakehouse_derived_view', 'lakehouse_keyword',
    'lakehouse_keyword_alias_vector', 'lakehouse_table_status', 'ont_import_log',
    'ont_knowledge', 'ont_knowledge_definition', 'ont_link_type', 'ont_metric',
    'ont_object_type', 'ont_property'
  ]) LOOP
    IF to_regclass('public.' || quote_ident(t)) IS NOT NULL THEN
      EXECUTE format('GRANT INSERT, UPDATE, DELETE ON TABLE public.%I TO collector_server_user', t);
    ELSE
      RAISE NOTICE 'collector_server_user: table public.% absent — skipping write grant', t;
    END IF;
  END LOOP;
END $$;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO collector_server_user;

-- collector-server finalizes an ingest by pointing the project at its lakehouse
-- schema (wizard confirm, pbit SSE import, and pbix extract all do
-- `UPDATE project SET lakehouse_schema=…`). Without this the project reads as
-- "not configured" and SmartQuery / the lakehouse-sql UI / the builder agent
-- see no data even though the tables + ontology landed. Column-scoped UPDATE
-- keeps it tight: collector cannot INSERT/DELETE projects or alter other columns.
DO $$
BEGIN
  IF to_regclass('public.project') IS NOT NULL THEN
    EXECUTE 'GRANT UPDATE (lakehouse_schema, source_type, status, updated_at) ON TABLE public.project TO collector_server_user';
  ELSE
    RAISE NOTICE 'collector_server_user: table public.project absent — skipping project UPDATE grant';
  END IF;
END $$;

-- 9. Sequence grants for INSERTing roles.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO
    backend_api_user, agent_server_user, lakehouse_sql_server_user;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO recall_server_user;

-- 10. ont_audit_log: durable sink for authmw audit events (Wave-5 P1-i). Every
-- service that runs the authmw audit middleware INSERTs here on each call
-- (backend-api, agent-server, recall-server, lakehouse-sql-server,
-- collector-server). mcp-tools-server uses its own auth and is excluded.
-- recall_server_user is otherwise read-only, so it needs an explicit INSERT +
-- sequence USAGE grant here. The DBAuditWriter degrades to stderr if these
-- grants are missing, so this is non-fatal — but grant it so the durable trail
-- actually lands. Idempotent + tolerant of the table being absent.
DO $$
DECLARE r text;
BEGIN
  IF to_regclass('public.ont_audit_log') IS NOT NULL THEN
    FOR r IN SELECT unnest(ARRAY[
      'backend_api_user', 'agent_server_user', 'recall_server_user',
      'lakehouse_sql_server_user', 'collector_server_user'
    ]) LOOP
      EXECUTE format('GRANT INSERT ON TABLE public.ont_audit_log TO %I', r);
      EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE public.ont_audit_log_id_seq TO %I', r);
    END LOOP;
    -- backend-api surfaces the audit trail read-side (admin views).
    EXECUTE 'GRANT SELECT ON TABLE public.ont_audit_log TO backend_api_user';
  ELSE
    RAISE NOTICE 'ont_audit_log absent — skipping audit grants';
  END IF;
END $$;

-- 11. Per-project proj_* schemas: self-healing owner + reader grants.
-- The normal path is collector_server_user creates these dynamically
-- (CREATE SCHEMA proj_<hex>) and pgschema.CreateSchemaWithGrants emits the
-- exact same set of grants below inline at creation time. But that path is
-- bypassed by anything that materialises a proj_* schema outside the
-- collector code: pg_dump restore with --no-owner --no-privileges, manual
-- psql, cross-environment migration, etc. Those schemas land owned by the
-- restoring superuser with zero grants, so lakehouse-sql-server /
-- agent-server / recall-server / backend-api see "relation does not exist"
-- the first time they touch a table in the schema (PG returns that — not
-- "permission denied" — when the role has no USAGE on the containing
-- schema, and the symptom is indistinguishable from a missing table).
--
-- Run every db-migrate boot — idempotent, cheap, and closes the loop so any
-- environment booted from a raw dump is self-healing on first migrate. New
-- PBIX imports are unaffected: collector's runtime grants still fire at
-- CREATE SCHEMA time; this block is the safety net for schemas that
-- arrived by other means.
DO $$
DECLARE
  s text;
  r record;
  readers constant text := 'lakehouse_sql_server_user, agent_server_user, recall_server_user, backend_api_user';
  reowned int := 0;
  granted int := 0;
BEGIN
  FOR s IN SELECT nspname FROM pg_namespace WHERE nspname LIKE 'proj_%' LOOP
    -- (a) Reown the schema + its objects to collector_server_user so future
    --     collector writes don't trip "permission denied for schema".
    IF (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = s) <> 'collector_server_user' THEN
      EXECUTE format('ALTER SCHEMA %I OWNER TO collector_server_user', s);
      FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = s LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO collector_server_user', s, r.tablename);
      END LOOP;
      FOR r IN SELECT sequencename FROM pg_sequences WHERE schemaname = s LOOP
        EXECUTE format('ALTER SEQUENCE %I.%I OWNER TO collector_server_user', s, r.sequencename);
      END LOOP;
      reowned := reowned + 1;
    END IF;

    -- (b) Reader grants — matches pgschema.CreateSchemaWithGrants exactly.
    --     USAGE on the schema, SELECT on every existing table, default-SELECT
    --     for tables collector_server_user creates later, sequence read.
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO %s', s, readers);
    EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO %s', s, readers);
    EXECUTE format('GRANT SELECT, USAGE ON ALL SEQUENCES IN SCHEMA %I TO %s', s, readers);
    EXECUTE format(
      'ALTER DEFAULT PRIVILEGES FOR ROLE collector_server_user IN SCHEMA %I GRANT SELECT ON TABLES TO %s',
      s, readers);
    granted := granted + 1;
  END LOOP;
  IF reowned > 0 THEN
    RAISE NOTICE 'proj_* self-heal: reowned % schemas to collector_server_user', reowned;
  END IF;
  IF granted > 0 THEN
    RAISE NOTICE 'proj_* self-heal: refreshed reader grants on % schemas', granted;
  END IF;
END $$;

COMMIT;

-- Verification (optional manual; also run by scripts/check-runtime-grants.sh):
--   SELECT has_table_privilege('backend_api_user', 'ont_agent_thread', 'UPDATE'); → f
--   SELECT has_table_privilege('agent_server_user', 'ont_agent_thread', 'UPDATE'); → t
--   SELECT has_table_privilege('recall_server_user', 'ont_agent_thread', 'SELECT'); → f
--   SELECT has_database_privilege('collector_server_user', current_database(), 'CREATE'); → t
--   SELECT has_table_privilege('collector_server_user', 'ingest_job', 'INSERT'); → t
