-- Lakehouse2Ontology Database Schema
-- Requires: pgvector extension
-- 2026-04-19: refactored from text2dax-ontology lakehouse-only branch — DuckDB,
-- PowerBI live, csv_datasource, and all DAX columns (dax_template, dax_params,
-- dax_expression, generated_dax) have been removed. Postgres is the sole OLAP
-- backend; smartquery generates Postgres SQL (not DAX).

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ==================== 1. user ====================
CREATE TABLE IF NOT EXISTS "user" (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        VARCHAR(100) UNIQUE NOT NULL,
    password_hash   VARCHAR(255) NOT NULL,
    display_name    VARCHAR(100),
    role            VARCHAR(20) DEFAULT 'user',
    is_active       BOOLEAN DEFAULT TRUE,
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- ==================== 1b. app_setting (global key-value) ====================
-- Process-wide settings not scoped to a project or user. Currently holds the
-- self-registration toggle. Read by /api/auth/registration-status (public) and
-- the /api/auth/register gate; written from the admin user-management page.
CREATE TABLE IF NOT EXISTS app_setting (
    key         VARCHAR(64) PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMPTZ DEFAULT now()
);
-- Registration is fail-closed by default; an admin enables it from /settings/users.
INSERT INTO app_setting (key, value) VALUES ('allow_registration', 'false')
ON CONFLICT (key) DO NOTHING;

-- ==================== 2. project ====================
CREATE TABLE IF NOT EXISTS project (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(200) NOT NULL,
    description     TEXT,
    owner_id        UUID NOT NULL REFERENCES "user"(id),
    source_type     VARCHAR(50),
    source_file     VARCHAR(500),
    compatibility   INTEGER,
    status          VARCHAR(20) DEFAULT 'active',
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- project_member: per-user access list for a project.
-- Without this row, the user cannot read or mutate any project-scoped
-- data via /api/*. The project owner is auto-added on project creation
-- (see backend-api/handler/handler_project.go); admin role can bypass
-- the membership check.
CREATE TABLE IF NOT EXISTS project_member (
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    role            VARCHAR(32) NOT NULL DEFAULT 'viewer',  -- owner | editor | viewer
    created_at      TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_project_member_user ON project_member (user_id);

-- Backfill owners. New rows inserted via INSERT ... SELECT WHERE NOT EXISTS
-- so re-running this script on a populated DB is idempotent.
INSERT INTO project_member (project_id, user_id, role)
SELECT id, owner_id, 'owner' FROM project
ON CONFLICT (project_id, user_id) DO NOTHING;

-- ==================== 10. prompt_config ====================
CREATE TABLE IF NOT EXISTS prompt_config (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    config_key      VARCHAR(50) NOT NULL,
    config_value    TEXT NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1,
    is_active       BOOLEAN DEFAULT FALSE,
    mark            BOOLEAN DEFAULT FALSE,
    note            TEXT,
    created_by      UUID REFERENCES "user"(id),
    updated_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, config_key, version)
);

-- ==================== 12. llm_config ====================
CREATE TABLE IF NOT EXISTS llm_config (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    config_type     VARCHAR(20) NOT NULL,  -- 'chat' | 'embedding'
    vendor          VARCHAR(50) NOT NULL,
    base_url        VARCHAR(500) NOT NULL,
    api_key         VARCHAR(500),
    model_name      VARCHAR(200) NOT NULL,
    is_thinking     BOOLEAN DEFAULT FALSE,
    vector_dim      INTEGER,
    is_active       BOOLEAN DEFAULT FALSE,
    note            TEXT,
    alias           VARCHAR(100),                 -- friendly user-facing label (UI title); falls back to model_name
    created_by      UUID REFERENCES "user"(id),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- ==================== Indexes ====================

-- Vector indexes (IVFFlat) — only create when rows exist; skip if already exists
-- We use DO blocks so CREATE INDEX IF NOT EXISTS works cleanly

CREATE INDEX IF NOT EXISTS idx_prompt_config_project ON prompt_config (project_id);

CREATE INDEX IF NOT EXISTS idx_llm_config_type ON llm_config (config_type);

-- Unique partial index for active prompt config
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_config_active
    ON prompt_config (project_id, config_key)
    WHERE is_active = TRUE;

-- ==================== Seed Data ====================
-- All use ON CONFLICT DO NOTHING so re-running is safe.

-- Abbreviations for readability
-- U = a0000000-0000-0000-0000-000000000001 (admin user)
--
-- No default projects are seeded: a fresh install opens with an empty
-- project list so the user lands in /setup-wizard and creates their own.
-- The previous AdventureWorks / Contoso Sales seeds (and their DAX-era
-- prompt_config rows) were removed with the DuckDB / PowerBI live tear-out.

-- 1. User
-- password_hash is intentionally the sentinel BOOTSTRAP_REQUIRED. On first
-- start, backend-api reads ADMIN_PASSWORD from the environment, hashes it,
-- and overwrites this row. Until that happens, login is impossible — this
-- is a deliberate fail-closed default. See core/auth_bootstrap.go.
INSERT INTO "user" (id, username, password_hash, display_name, role)
VALUES ('a0000000-0000-0000-0000-000000000001', 'admin',
        'BOOTSTRAP_REQUIRED',
        '管理员', 'admin')
ON CONFLICT (username) DO NOTHING;

-- 2. Project — intentionally empty. See header note above.

-- 10. Prompt Configs — intentionally empty. The legacy seeds referenced the
-- removed AdventureWorks project AND were DAX-era prompts (system was
-- DuckDB+DAX before the lakehouse2ontology rewrite). New projects either
-- inherit defaults at creation time or rely on the global prompt_config.

-- ==================== ALTER: llm_config add is_tool_call ====================
ALTER TABLE llm_config ADD COLUMN IF NOT EXISTS is_tool_call BOOLEAN DEFAULT FALSE;

-- proxy support for LLM config
ALTER TABLE llm_config ADD COLUMN IF NOT EXISTS proxy_url VARCHAR(500);

-- Drop the old unique-active-per-type constraint (now multiple active configs allowed for different roles)
DROP INDEX IF EXISTS idx_llm_config_active;

-- ==================== 14. llm_role_binding ====================
CREATE TABLE IF NOT EXISTS llm_role_binding (
    role_name   VARCHAR(30) PRIMARY KEY,
    config_id   UUID NOT NULL REFERENCES llm_config(id) ON DELETE CASCADE,
    updated_at  TIMESTAMPTZ DEFAULT now()
);

-- ==============================================================================
-- ONTOLOGY TABLES (ont_ prefix) — Ontology-Based Deterministic Query System
-- ==============================================================================

-- ==================== ont_2. ont_object_type ====================
CREATE TABLE IF NOT EXISTS ont_object_type (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    name         VARCHAR(200) NOT NULL,
    display_name VARCHAR(200),
    kind         VARCHAR(20) NOT NULL DEFAULT 'entity', -- entity | event | attribute
    description  TEXT,
    source_table VARCHAR(200),
    source_config JSONB DEFAULT '{}',
    bridged_from UUID,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_by   UUID REFERENCES "user"(id),
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, name)
);


-- ==================== ont_3. ont_property ====================
CREATE TABLE IF NOT EXISTS ont_property (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    object_type_id UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    name           VARCHAR(200) NOT NULL,
    display_name   VARCHAR(200),
    data_type      VARCHAR(50),
    source_column  VARCHAR(200),
    is_filterable  BOOLEAN DEFAULT TRUE,
    is_groupable   BOOLEAN DEFAULT TRUE,
    enum_values    TEXT[],
    description    TEXT,
    short_description TEXT,
    prop_vector    vector(1024),
    bridged_from   UUID,
    is_machine_code BOOLEAN DEFAULT FALSE,
    keywords_synced_at TIMESTAMPTZ,
    mark           BOOLEAN DEFAULT FALSE,
    note           TEXT,
    created_by     UUID REFERENCES "user"(id),
    created_at     TIMESTAMPTZ DEFAULT now(),
    updated_at     TIMESTAMPTZ DEFAULT now(),
    UNIQUE (object_type_id, name)
);

-- ==================== ont_3b. lakehouse_keyword ====================
-- Keyword → lakehouse entity mapping. Each keyword points to EITHER a property
-- (column/value on an Od) OR a metric intent (canonical query template),
-- enforced by CHECK constraint. property_id is nullable since an intent-backed
-- keyword has no direct property anchor.
CREATE TABLE IF NOT EXISTS lakehouse_keyword (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    object_type_id UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    property_id    UUID REFERENCES ont_property(id) ON DELETE CASCADE,
    metric_intent_id UUID,  -- FK added below (forward reference)
    keyword        TEXT NOT NULL,
    is_machine_code BOOLEAN DEFAULT FALSE,
    synced_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE (property_id, keyword),
    CHECK (property_id IS NOT NULL OR metric_intent_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_project ON lakehouse_keyword(project_id);
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_keyword ON lakehouse_keyword(keyword);
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_intent
    ON lakehouse_keyword(metric_intent_id) WHERE metric_intent_id IS NOT NULL;

-- ==================== ont_3c. lakehouse_metric_intent ====================
-- "Query intent shortcut" layer above Od/Property. Maps natural-language
-- terms (via lakehouse_keyword) to a canonical smartquery template so the LLM
-- doesn't have to re-derive filter/groupBy semantics from prose.
--
-- Example: "early order" (colloquial) → Order.Total
--   canonical_metric  = "sum(Order_Quantity)"
--   canonical_filters = []
--   auto_group_by     = ["Order_Type"]   -- MUST stay in groupBy, never filter
--   response_template = "共 {total} pcs，其中 {real} 已转 Real Order"
--
-- canonical_filters JSON shape: [{"prop":"Order_Type","op":"=","value":"Real Order"}]
CREATE TABLE IF NOT EXISTS lakehouse_metric_intent (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    object_id         UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    display_name      TEXT,
    canonical_metric  TEXT NOT NULL,
    canonical_filters JSONB DEFAULT '[]'::jsonb,
    auto_group_by     TEXT[] DEFAULT '{}',
    -- pivot_on: if set, the smartquery executor post-processes the result JSON
    -- by pivoting this column into wide-format columns. pivot_values fixes the
    -- column order (e.g. ['Early Order','Real Order']); absent values fall back
    -- to whatever distinct values appear in data. pivot_total_label names the
    -- synthetic sum column appended at the end.
    pivot_on          TEXT,
    pivot_values      TEXT[],
    pivot_total_label TEXT DEFAULT 'Total',
    response_template TEXT,
    description       TEXT,
    priority          INT DEFAULT 0,
    mark              BOOLEAN DEFAULT TRUE,
    created_at        TIMESTAMPTZ DEFAULT now(),
    updated_at        TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, name)
);
-- Forward-migration for existing DBs (CREATE TABLE IF NOT EXISTS is a no-op).
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_on                  TEXT;
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_values              TEXT[];
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_total_label         TEXT DEFAULT 'Total';
-- pivot_column_labels: per-value display names, parallel to pivot_values.
--   Example: pivot_values=['Early Order','Real Order']
--            pivot_column_labels=['未转化的early Order','已转化的early Order（业务术语Real Order）']
-- pivot_with_percent: after each value column append a "{label} 占比" column.
-- pivot_append_grand_total: append a final summary row with aggregate counts
--   across all dim-tuples. Other dim columns in this row are set to literal
--   "合计" (or the totalLabel if dim has only one column).
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_column_labels      TEXT[];
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_with_percent       BOOLEAN DEFAULT FALSE;
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_append_grand_total BOOLEAN DEFAULT FALSE;
-- pivot_percent_axis: direction of percentage calculation.
--   'row'    = value / rowTotal (within-row structure, default)
--   'column' = value / columnTotal (cross-row contribution)
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_percent_axis TEXT DEFAULT 'row';
-- pivot_percent_scope: denominator scope for percentage calculation.
--   'filtered' = denominator uses the current (possibly filtered) result set (default)
--   'global'   = denominator uses an unfiltered query (canonical_filters only, no user filters)
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_percent_scope TEXT NOT NULL DEFAULT 'filtered';
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS pivot_percent_suffix TEXT DEFAULT '占比';
-- replace_group_by: when true, enforceIntentAutoGroupBy OVERWRITES spec.GroupBy
-- with auto_group_by (instead of prepending missing props). Used by single-dim
-- share Intents (e.g. Order.Quantity.BrandShareInGen) where extra groupBy dims
-- added by the LLM would split the share pool (e.g. BRAND × Order_Type) —
-- the Intent's semantics require sharing at exactly auto_group_by granularity.
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS replace_group_by BOOLEAN NOT NULL DEFAULT false;
-- P6: Intent owns full query shape (default ORDER BY / LIMIT) and declares
-- user-level parameters so agent-server can synthesise a focused tool def
-- per matched intent. Parameters JSONB schema convention:
-- {label, kind, options?, required?} per parameter.
--
-- Optional per-parameter key `shapeCapability` (text) is a soft reference to
-- lakehouse_shape_capability.name — the mission reachability gate uses it to
-- verify the LLM's coverage claim against the parameter's declared shape.
-- Empty / missing = "shape unknown", gate degrades to LLM-only judgement.
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS default_order_by_label TEXT;
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS default_order_by_dir   TEXT;
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS default_limit          INT;
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS parameters             JSONB NOT NULL DEFAULT '[]'::jsonb;
-- plan: composite-Intent step DAG. NULL = ordinary single-query Intent
-- (canonical_metric / canonical_filters / auto_group_by path); non-null = the
-- query is a multi-step plan and is routed to /internal/smartquery/execute-plan
-- instead of /internal/smartquery/execute. See .omc/specs/plan-mode-composite-intent.md.
ALTER TABLE lakehouse_metric_intent ADD COLUMN IF NOT EXISTS plan                   JSONB;
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.constraint_column_usage
    WHERE table_name = 'lakehouse_metric_intent' AND constraint_name = 'lakehouse_metric_intent_default_order_by_dir_chk'
  ) THEN
    ALTER TABLE lakehouse_metric_intent
      ADD CONSTRAINT lakehouse_metric_intent_default_order_by_dir_chk
      CHECK (default_order_by_dir IS NULL OR default_order_by_dir IN ('ASC','DESC'));
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS idx_lh_intent_project
    ON lakehouse_metric_intent(project_id);
CREATE INDEX IF NOT EXISTS idx_lh_intent_object
    ON lakehouse_metric_intent(object_id);

-- ── Forward-migration for existing databases ────────────────────────────
-- On a fresh DB the inline column+CHECK above suffices. On an existing DB,
-- CREATE TABLE IF NOT EXISTS is a no-op, so we add the new column, drop the
-- obsolete NOT NULL on property_id, and add the CHECK + FK here.
ALTER TABLE lakehouse_keyword
    ADD COLUMN IF NOT EXISTS metric_intent_id UUID;
ALTER TABLE lakehouse_keyword
    ALTER COLUMN property_id DROP NOT NULL;

DO $$ BEGIN
    ALTER TABLE lakehouse_keyword
        ADD CONSTRAINT lakehouse_keyword_metric_intent_id_fkey
        FOREIGN KEY (metric_intent_id)
        REFERENCES lakehouse_metric_intent(id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Keyword triage (2026-04-17): Od-alias anchor + stopword flag.
-- A single keyword string may appear in multiple rows, each with a different
-- binding (property / object / metric intent). is_stopword rows are skipped
-- by recall. See docs/keyword-triage-spec.md.
ALTER TABLE lakehouse_keyword
    ADD COLUMN IF NOT EXISTS object_id   UUID REFERENCES ont_object_type(id) ON DELETE CASCADE;
ALTER TABLE lakehouse_keyword
    ADD COLUMN IF NOT EXISTS is_stopword BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE lakehouse_keyword
    DROP CONSTRAINT IF EXISTS lakehouse_keyword_anchor_chk;
-- The original inline CHECK from CREATE TABLE is auto-named
-- "lakehouse_keyword_check" by PostgreSQL. Drop that legacy name too —
-- otherwise the old `property_id IS NOT NULL OR metric_intent_id IS NOT NULL`
-- check coexists and rejects stopword-only rows.
ALTER TABLE lakehouse_keyword
    DROP CONSTRAINT IF EXISTS lakehouse_keyword_check;

DO $$ BEGIN
    ALTER TABLE lakehouse_keyword
        ADD CONSTRAINT lakehouse_keyword_anchor_chk
        CHECK (
            is_stopword = TRUE
            OR property_id      IS NOT NULL
            OR object_id        IS NOT NULL
            OR metric_intent_id IS NOT NULL
        );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_intent
    ON lakehouse_keyword(metric_intent_id) WHERE metric_intent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_lower
    ON lakehouse_keyword(project_id, LOWER(keyword));
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_object
    ON lakehouse_keyword(object_id) WHERE object_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_stopword
    ON lakehouse_keyword(project_id) WHERE is_stopword = TRUE;

-- ==================== ont_3d. lakehouse_metric (unified 指标) ====================
-- The unified "指标" concept: a query definition built ON TOP OF a selected Od
-- ("metric → od → ontologysql → lakehousesql"). It folds together what used to
-- be three muddled concepts — 度量 (ont_metric) / 指标 / 意图 (lakehouse_metric_intent):
--   * a metric BUNDLES its Od (object_id) — selecting the metric implies the Od;
--   * it is a structured, Od-grounded query definition (NOT raw SQL — the LLM
--     never writes SQL; the SmartQuery compiler still emits it from the Od graph);
--   * it declares typed REQUIRED + OPTIONAL parameters: user filters fill optional
--     params; a missing required param makes the agent ASK the user (don't guess).
--
-- This table is a clean SUPERSET of lakehouse_metric_intent (flat pivot columns are
-- kept identical so the recall renderer + SmartQuery executor are reused UNCHANGED
-- via an adapter) plus headroom columns (level / status / schema_version /
-- definition / extra / deleted_at). It COEXISTS with lakehouse_metric_intent during
-- the compatibility window; data migration + cutover + deprecation of the old table
-- is a SEPARATE later phase. canonical_metric stays NOT NULL (aggregation mandatory;
-- projection-only metrics are out of scope). plan (level='plan') is the multi-step DAG.
CREATE TABLE IF NOT EXISTS lakehouse_metric (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id               UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    object_id                UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,
    display_name             TEXT,
    description              TEXT,
    level                    TEXT NOT NULL DEFAULT 'simple',   -- 'simple' | 'plan' | 'sql'
    canonical_metric         TEXT NOT NULL,    -- level='sql' stores sentinel '(sql)'
    query_sql                TEXT,             -- level='sql': human SQL (Od names + {{params}})
    canonical_filters        JSONB NOT NULL DEFAULT '[]'::jsonb,
    auto_group_by            TEXT[] NOT NULL DEFAULT '{}',
    replace_group_by         BOOLEAN NOT NULL DEFAULT false,
    default_order_by_label   TEXT,
    default_order_by_dir     TEXT,
    default_limit            INT,
    -- pivot (flat — identical to lakehouse_metric_intent so renderer/IntentHint reuse):
    pivot_on                 TEXT,
    pivot_values             TEXT[],
    pivot_column_labels      TEXT[],
    pivot_total_label        TEXT DEFAULT 'Total',
    pivot_with_percent       BOOLEAN DEFAULT FALSE,
    pivot_append_grand_total BOOLEAN DEFAULT FALSE,
    pivot_percent_axis       TEXT DEFAULT 'row',      -- 'row' | 'column'
    pivot_percent_scope      TEXT NOT NULL DEFAULT 'filtered', -- 'filtered' | 'global'
    pivot_percent_suffix     TEXT DEFAULT '占比',
    -- parameter contract: [{name,type,property,op,optional,default,description,...}]
    parameters               JSONB NOT NULL DEFAULT '[]'::jsonb,
    plan                     JSONB,                   -- level='plan' step DAG; NULL = single-query
    response_template        TEXT,
    priority                 INT NOT NULL DEFAULT 0,
    mark                     BOOLEAN NOT NULL DEFAULT TRUE,  -- active gate (this branch)
    -- headroom (reserved; not yet load-bearing this branch):
    status                   TEXT NOT NULL DEFAULT 'active',
    schema_version           INT NOT NULL DEFAULT 1,
    definition               JSONB NOT NULL DEFAULT '{}'::jsonb,
    extra                    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by               UUID REFERENCES "user"(id),
    created_at               TIMESTAMPTZ DEFAULT now(),
    updated_at               TIMESTAMPTZ DEFAULT now(),
    deleted_at               TIMESTAMPTZ,
    CONSTRAINT lakehouse_metric_default_order_by_dir_chk
        CHECK (default_order_by_dir IS NULL OR default_order_by_dir IN ('ASC','DESC')),
    UNIQUE (project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_lh_metric_project ON lakehouse_metric(project_id);
CREATE INDEX IF NOT EXISTS idx_lh_metric_object  ON lakehouse_metric(object_id);

-- Trigger-keyword link to the unified metric (mirrors metric_intent_id). A keyword
-- row with non-null metric_id makes that metric visible to recall.
ALTER TABLE lakehouse_keyword
    ADD COLUMN IF NOT EXISTS metric_id UUID;
DO $$ BEGIN
    ALTER TABLE lakehouse_keyword
        ADD CONSTRAINT lakehouse_keyword_metric_id_fkey
        FOREIGN KEY (metric_id)
        REFERENCES lakehouse_metric(id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Relax the anchor CHECK to also accept a metric_id anchor (supersedes the
-- earlier definition above — drop + re-add wins on a top-to-bottom run).
ALTER TABLE lakehouse_keyword
    DROP CONSTRAINT IF EXISTS lakehouse_keyword_anchor_chk;
DO $$ BEGIN
    ALTER TABLE lakehouse_keyword
        ADD CONSTRAINT lakehouse_keyword_anchor_chk
        CHECK (
            is_stopword = TRUE
            OR property_id      IS NOT NULL
            OR object_id        IS NOT NULL
            OR metric_intent_id IS NOT NULL
            OR metric_id        IS NOT NULL
        );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_metric
    ON lakehouse_keyword(metric_id) WHERE metric_id IS NOT NULL;

-- ==================== ont_4. ont_link_type ====================
CREATE TABLE IF NOT EXISTS ont_link_type (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    from_object_id UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    to_object_id   UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    link_name      VARCHAR(200),
    fk_column      VARCHAR(200),
    cardinality    VARCHAR(20) NOT NULL,
    reject_reason  TEXT,
    description    TEXT,
    bridged_from   UUID,
    mark           BOOLEAN DEFAULT TRUE,
    note           TEXT,
    created_by     UUID REFERENCES "user"(id),
    created_at     TIMESTAMPTZ DEFAULT now(),
    updated_at     TIMESTAMPTZ DEFAULT now()
);

-- ==================== ont_5. ont_metric ====================
CREATE TABLE IF NOT EXISTS ont_metric (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    name             VARCHAR(200) NOT NULL,
    display_name     VARCHAR(200),
    metric_type      VARCHAR(20) NOT NULL DEFAULT 'simple',
    aggregation      VARCHAR(50),
    target_object_id UUID REFERENCES ont_object_type(id),
    target_property  VARCHAR(200),
    formula          TEXT,
    depends_on       TEXT[],
    format_string    VARCHAR(100),
    description      TEXT,
    metric_vector    vector(1024),
    bridged_from     UUID,
    mark             BOOLEAN DEFAULT FALSE,
    note             TEXT,
    created_by       UUID REFERENCES "user"(id),
    created_at       TIMESTAMPTZ DEFAULT now(),
    updated_at       TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, name)
);

-- ==================== ont_6. ont_alias ====================
CREATE TABLE IF NOT EXISTS ont_alias (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    alias_text   VARCHAR(200) NOT NULL,
    alias_type   VARCHAR(30) NOT NULL,
    target_id    UUID,
    target_kind  VARCHAR(30),
    canonical_value VARCHAR(256),
    ambiguity_config JSONB,
    is_exact_match BOOLEAN DEFAULT FALSE,
    priority     INT DEFAULT 0,
    synonyms     TEXT[],
    alias_vector vector(1024),
    bridged_from UUID,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_by   UUID REFERENCES "user"(id),
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);

-- ==================== ont_7. ont_resolution_rule ====================
CREATE TABLE IF NOT EXISTS ont_resolution_rule (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    rule_type    VARCHAR(32) NOT NULL,
    trigger_key  VARCHAR(128) NOT NULL,
    rule_config  JSONB NOT NULL,
    priority     INT DEFAULT 0,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now()
);

-- ==================== ont_8. ont_method ====================
CREATE TABLE IF NOT EXISTS ont_method (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    method_name  VARCHAR(50) NOT NULL,
    display_name VARCHAR(100),
    description  TEXT,
    trigger_words JSONB,
    parameters   JSONB DEFAULT '{}',
    execution_config JSONB DEFAULT '{}',
    is_enabled   BOOLEAN DEFAULT TRUE,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, method_name)
);

-- ==================== ont_10. ont_query_log ====================
CREATE TABLE IF NOT EXISTS ont_query_log (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id         UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    user_question      TEXT NOT NULL,
    tokens             JSONB,
    intent_signals     JSONB,
    vector_hits        JSONB,
    anchor_result      JSONB,
    disambig_result    JSONB,
    method_call        JSONB,
    execution_status   VARCHAR(20),
    execution_result   TEXT,
    execution_error    TEXT,
    execution_duration FLOAT,
    stage_latencies    JSONB,
    summary            TEXT,
    confidence         FLOAT,
    used_llm           BOOLEAN DEFAULT FALSE,
    model_name         VARCHAR(100),
    mark               BOOLEAN DEFAULT FALSE,
    note               TEXT,
    objects            TEXT DEFAULT '',
    metric             TEXT DEFAULT '',
    group_by           TEXT DEFAULT '',
    question_vector    vector(1024),
    is_example         BOOLEAN DEFAULT FALSE,
    created_by         UUID REFERENCES "user"(id),
    created_at         TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_qlog_project ON ont_query_log (project_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ont_qlog_unique_question ON ont_query_log (project_id, user_question);

-- ==================== ont_token_annotation ====================
CREATE TABLE IF NOT EXISTS ont_token_annotation (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    token         TEXT NOT NULL,
    object_name   TEXT NOT NULL DEFAULT '',
    property_name TEXT NOT NULL DEFAULT '',
    metric_name   TEXT NOT NULL DEFAULT '',
    note          TEXT DEFAULT '',
    embedding     vector(1024),
    mark          BOOLEAN DEFAULT TRUE,
    created_at    TIMESTAMPTZ DEFAULT now(),
    updated_at    TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_token_ann_project ON ont_token_annotation (project_id);

-- ==================== ont_agent_thread + ont_agent_step ====================
CREATE TABLE IF NOT EXISTS ont_agent_thread (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    title       TEXT DEFAULT '',
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_thread_project ON ont_agent_thread (project_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS ont_agent_step (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id         UUID NOT NULL REFERENCES ont_agent_thread(id) ON DELETE CASCADE,
    step_index        INT NOT NULL DEFAULT 0,
    role              VARCHAR(20) NOT NULL,
    content           TEXT DEFAULT '',
    thinking          TEXT DEFAULT '',
    function_call     JSONB,
    system_prompt     TEXT DEFAULT '',
    llm_messages      JSONB,
    tokens            JSONB,
    prompt_tokens     INT DEFAULT 0,
    completion_tokens INT DEFAULT 0,
    total_tokens      INT DEFAULT 0,
    duration_ms       INT DEFAULT 0,
    created_at        TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_agent_step_thread ON ont_agent_step (thread_id, step_index);

-- ── MissionAct (see .omc/specs/mission-act.md) ───────────────────────
-- A mission is the unified per-turn state object: one row per user
-- message. Single-query / compose / plan / capability_gap are all just
-- different shapes of `state` (the full mission JSON).
CREATE TABLE IF NOT EXISTS ont_mission (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id          UUID NOT NULL REFERENCES ont_agent_thread(id) ON DELETE CASCADE,
    parent_mission_id  UUID REFERENCES ont_mission(id) ON DELETE SET NULL,
    project_id         UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    question           TEXT NOT NULL DEFAULT '',
    state              JSONB NOT NULL DEFAULT '{}'::jsonb,
    status             VARCHAR(16) NOT NULL DEFAULT 'active',
    created_at         TIMESTAMPTZ DEFAULT now(),
    updated_at         TIMESTAMPTZ DEFAULT now()
);
ALTER TABLE ont_mission DROP CONSTRAINT IF EXISTS ont_mission_status_chk;
ALTER TABLE ont_mission ADD CONSTRAINT ont_mission_status_chk
    CHECK (status IN ('active','complete','partial','unanswerable'));
CREATE INDEX IF NOT EXISTS idx_ont_mission_thread ON ont_mission (thread_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ont_mission_unanswerable
    ON ont_mission (project_id, status) WHERE status = 'unanswerable';

-- A declared + verified capability gap: a question dimension no Intent
-- can serve. Not a log — a backlog signal for the ontology authors.
CREATE TABLE IF NOT EXISTS capability_gap_log (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id         UUID NOT NULL REFERENCES ont_mission(id) ON DELETE CASCADE,
    project_id         UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    intent_name        TEXT,
    missing_dimension  TEXT NOT NULL,
    gap_kind           VARCHAR(20) NOT NULL,
    suggested_fix      TEXT DEFAULT '',
    evidence           JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at         TIMESTAMPTZ DEFAULT now()
);
ALTER TABLE capability_gap_log DROP CONSTRAINT IF EXISTS capability_gap_kind_chk;
ALTER TABLE capability_gap_log ADD CONSTRAINT capability_gap_kind_chk
    CHECK (gap_kind IN ('no_param','shape_unsupported','no_data'));
CREATE INDEX IF NOT EXISTS idx_capability_gap_dim
    ON capability_gap_log (project_id, missing_dimension);

-- Link agent steps to the mission they dispatched under. NULL for
-- pre-MissionAct rows — fully backward compatible.
ALTER TABLE ont_agent_step ADD COLUMN IF NOT EXISTS mission_id UUID
    REFERENCES ont_mission(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_agent_step_mission
    ON ont_agent_step (mission_id) WHERE mission_id IS NOT NULL;
-- ─────────────────────────────────────────────────────────────────────

-- agent_type on ont_agent_thread (lakehouse, sql, workbench)
ALTER TABLE ont_agent_thread ADD COLUMN IF NOT EXISTS agent_type VARCHAR(10) DEFAULT 'lakehouse';
ALTER TABLE ont_agent_thread DROP CONSTRAINT IF EXISTS agent_type_chk;
ALTER TABLE ont_agent_thread
  ADD CONSTRAINT agent_type_chk
  CHECK (agent_type IN ('lakehouse','builder'));

-- thread_state stores session state (todoItems, loadedSkills, sessionId) as JSONB
ALTER TABLE ont_agent_thread ADD COLUMN IF NOT EXISTS thread_state JSONB DEFAULT '{}';

-- source_type on ont_object_type
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS source_type VARCHAR(30) DEFAULT 'powerbi';

-- data_source_id (the data_source instance an object was imported from) is added
-- AFTER the data_source table is created below — search "data_source_id on
-- ont_object_type". It must not live here: this point runs ~650 lines before
-- data_source exists, which aborts a fresh schema init under psql ON_ERROR_STOP=1.

-- generated_sql on ont_query_log
ALTER TABLE ont_query_log ADD COLUMN IF NOT EXISTS generated_sql TEXT;
ALTER TABLE ont_query_log ADD COLUMN IF NOT EXISTS source_type VARCHAR(20) DEFAULT 'pipeline';
ALTER TABLE ont_query_log ADD COLUMN IF NOT EXISTS source VARCHAR(20) DEFAULT 'chat';
ALTER TABLE ont_query_log ADD COLUMN IF NOT EXISTS test_suite_id UUID;

-- ==================== ont_11. ont_query_feedback ====================
CREATE TABLE IF NOT EXISTS ont_query_feedback (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    query_log_id  UUID NOT NULL REFERENCES ont_query_log(id) ON DELETE CASCADE,
    feedback_type VARCHAR(20) NOT NULL,
    correction    JSONB,
    comment       TEXT,
    created_by    UUID REFERENCES "user"(id),
    created_at    TIMESTAMPTZ DEFAULT now()
);

-- ==================== ont_12. ont_test_suite ====================
CREATE TABLE IF NOT EXISTS ont_test_suite (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    status      VARCHAR(20) DEFAULT 'idle',
    total       INT DEFAULT 0,
    passed      INT DEFAULT 0,
    failed      INT DEFAULT 0,
    last_run_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_test_suite_project ON ont_test_suite (project_id, created_at DESC);

-- ==================== ont_13. ont_test_case ====================
CREATE TABLE IF NOT EXISTS ont_test_case (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    suite_id          UUID NOT NULL REFERENCES ont_test_suite(id) ON DELETE CASCADE,
    user_question     TEXT NOT NULL,
    sort_order        INT DEFAULT 0,
    status            VARCHAR(20) DEFAULT 'pending',
    function_calls    JSONB,
    final_answer      TEXT,
    execution_status  VARCHAR(20),
    execution_result  TEXT,
    execution_error   TEXT,
    duration_ms       INT DEFAULT 0,
    model_name        VARCHAR(100),
    prompt_tokens     INT DEFAULT 0,
    completion_tokens INT DEFAULT 0,
    total_tokens      INT DEFAULT 0,
    mark              VARCHAR(10),
    created_at        TIMESTAMPTZ DEFAULT now(),
    updated_at        TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_test_case_suite ON ont_test_case (suite_id, sort_order);

-- ont_test_suite extensions (lakehouse dataset testing)
ALTER TABLE ont_test_suite ADD COLUMN IF NOT EXISTS source_type VARCHAR(20) DEFAULT 'pipeline';
ALTER TABLE ont_test_suite ADD COLUMN IF NOT EXISTS concurrency INT DEFAULT 1;

-- ont_test_case extensions (lakehouse dataset testing)
ALTER TABLE ont_test_case ADD COLUMN IF NOT EXISTS code VARCHAR(20) DEFAULT '';
ALTER TABLE ont_test_case ADD COLUMN IF NOT EXISTS generated_sql TEXT DEFAULT '';
-- 模板级"正确答案"（参考答案）：仅作为人工对照，不参与自动判分。run snapshot 不冗余。
ALTER TABLE ont_test_case ADD COLUMN IF NOT EXISTS expected_answer TEXT DEFAULT '';

-- ==================== ont_14. ont_topic ====================
CREATE TABLE IF NOT EXISTS ont_topic (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    parent_id  UUID REFERENCES ont_topic(id) ON DELETE SET NULL,
    name       VARCHAR(200) NOT NULL,
    summary    TEXT,
    sort_order INT DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_ont_topic_project ON ont_topic(project_id);

-- ==================== ont_15. ont_knowledge ====================
-- entry_type / anchor_type / skill_config conventions (no CHECK — values are
-- documented here so the application layer can validate):
--   entry_type values:
--     'concept'          (default) free-form knowledge entry
--     'analysis'         analysis-pattern skill card — drives plan-mode in
--                        agent-server. See .omc/specs/plan-from-ontology-knowledge.md
--   anchor_type values:
--     'version' (default) | 'object' | 'property' | 'metric' | 'analysis_pattern'
--   skill_config JSONB schema for entry_type='analysis' (analysis pattern card):
--     {
--       "trigger": { "keywords":[...], "structural_hints":[...] },
--       "features": [
--         { "id":"<feature-id>", "behavior":"<one-line description>",
--           "verification":"<predicate over result>",
--           "tool_hints":[
--             {"tool":"smartquery","intent":"<intent-name>"},
--             {"tool":"compose_query"},
--             {"tool":"query_dag","ref":"<intent-name>"}
--           ]
--         }, ...
--       ],
--       "synthesis": {
--         "template": "<Go text/template, {{ .features.<id>.summary|rows|value|error }} vars>",
--         "caveats": ["<verbatim caveat 1>", ...]
--       }
--     }
-- See .omc/specs/plan-from-ontology-knowledge.md §3.1 for full semantics.
-- Triggering keywords for these cards live in ont_knowledge_keyword (below) —
-- the same table used for ordinary OK keyword recall.
CREATE TABLE IF NOT EXISTS ont_knowledge (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    topic_id     UUID REFERENCES ont_topic(id) ON DELETE SET NULL,
    parent_id    UUID REFERENCES ont_knowledge(id) ON DELETE SET NULL,
    title        VARCHAR(300) NOT NULL,
    summary      TEXT,
    content      TEXT,
    entry_type   VARCHAR(20) NOT NULL DEFAULT 'concept',
    anchor_type  VARCHAR(20) DEFAULT 'version',
    anchor_id    UUID,
    anchor_ids          UUID[] DEFAULT '{}',
    linked_property_id  UUID REFERENCES ont_property(id) ON DELETE SET NULL,
    skill_config JSONB DEFAULT '{}',
    sort_order   INT DEFAULT 0,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_knowledge_project ON ont_knowledge(project_id);
CREATE INDEX IF NOT EXISTS idx_ont_knowledge_topic   ON ont_knowledge(topic_id);
CREATE INDEX IF NOT EXISTS idx_ont_knowledge_anchor  ON ont_knowledge(anchor_type, anchor_id);

-- ==================== ont_16. ont_causality ====================
CREATE TABLE IF NOT EXISTS ont_causality (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    from_knowledge_id UUID NOT NULL REFERENCES ont_knowledge(id) ON DELETE CASCADE,
    to_knowledge_id   UUID NOT NULL REFERENCES ont_knowledge(id) ON DELETE CASCADE,
    relation_type     VARCHAR(20) NOT NULL DEFAULT 'correlates',
    direction         VARCHAR(20) NOT NULL DEFAULT 'neutral',
    description       TEXT,
    sort_order        INT DEFAULT 0,
    mark              BOOLEAN DEFAULT FALSE,
    note              TEXT,
    created_at        TIMESTAMPTZ DEFAULT now(),
    updated_at        TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_causality_project ON ont_causality(project_id);

-- ==================== ont_16b. ont_knowledge_definition ====================
CREATE TABLE IF NOT EXISTS ont_knowledge_definition (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_id UUID NOT NULL REFERENCES ont_knowledge(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    def_type     VARCHAR(20) NOT NULL DEFAULT 'positive', -- 'positive' | 'misconception'
    content      TEXT,
    sort_order   INT DEFAULT 0,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_kdef_knowledge ON ont_knowledge_definition(knowledge_id);

-- ==================== ont_16c. ont_knowledge_example ====================
CREATE TABLE IF NOT EXISTS ont_knowledge_example (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_id UUID NOT NULL REFERENCES ont_knowledge(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    example_type VARCHAR(20) NOT NULL DEFAULT 'knowledge', -- 'knowledge' | 'return'
    content      TEXT,
    sort_order   INT DEFAULT 0,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_kex_knowledge ON ont_knowledge_example(knowledge_id);

-- (ont_action_log table + 2 indexes removed — no Go references)

-- ==================== ont_18. ont_skill ====================
-- Skill definitions for the workbench agent (Layer 1/2 injection pattern).
-- Built-in skills are registered in Go code; user-defined skills stored here.
CREATE TABLE IF NOT EXISTS ont_skill (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    skill_name   VARCHAR(100) NOT NULL,
    display_name VARCHAR(200),
    description  TEXT,
    skill_body   TEXT,
    tools        JSONB DEFAULT '[]',
    is_enabled   BOOLEAN DEFAULT TRUE,
    sort_order   INT DEFAULT 0,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, skill_name)
);
CREATE INDEX IF NOT EXISTS idx_ont_skill_project ON ont_skill(project_id);

-- ==================== ont_19. ont_learned_fact (Ol) ====================
-- Learned facts from AI-user conversations. Created only by AI, confirmed by user.
CREATE TABLE IF NOT EXISTS ont_learned_fact (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    summary          TEXT NOT NULL,
    content          TEXT,
    confidence       VARCHAR(20) NOT NULL DEFAULT 'pending',
    source_thread_id UUID,
    source_type      VARCHAR(20) NOT NULL DEFAULT 'workbench',
    sort_order       INT DEFAULT 0,
    mark             BOOLEAN DEFAULT FALSE,
    note             TEXT,
    title            TEXT NOT NULL DEFAULT '',
    keywords         TEXT NOT NULL DEFAULT '',
    tags             TEXT[] NOT NULL DEFAULT '{}',
    content_vector   vector(1024),
    created_at       TIMESTAMPTZ DEFAULT now(),
    updated_at       TIMESTAMPTZ DEFAULT now()
);
-- Migration for existing installs: add tags column if missing
ALTER TABLE ont_learned_fact ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE ont_learned_fact ADD COLUMN IF NOT EXISTS fact_type VARCHAR(30) NOT NULL DEFAULT 'business_rule';
CREATE INDEX IF NOT EXISTS idx_ont_fact_project ON ont_learned_fact(project_id);
CREATE INDEX IF NOT EXISTS idx_ont_fact_confidence ON ont_learned_fact(confidence);
CREATE INDEX IF NOT EXISTS idx_ont_fact_vector ON ont_learned_fact USING ivfflat (content_vector vector_cosine_ops) WITH (lists = 10);
CREATE INDEX IF NOT EXISTS idx_ont_fact_tags ON ont_learned_fact USING GIN (tags);

-- ==================== ont_19b. ont_fact_definition ====================
CREATE TABLE IF NOT EXISTS ont_fact_definition (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fact_id      UUID NOT NULL REFERENCES ont_learned_fact(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    def_type     VARCHAR(20) NOT NULL DEFAULT 'positive',
    content      TEXT NOT NULL,
    sort_order   INT DEFAULT 0,
    mark         BOOLEAN DEFAULT FALSE,
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now(),
    updated_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_fdef_fact ON ont_fact_definition(fact_id);

-- ==================== ont_19c. ont_fact_link ====================
-- Many-to-many links from learned facts to Od/Ok/Ol entities.
CREATE TABLE IF NOT EXISTS ont_fact_link (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    fact_id      UUID NOT NULL REFERENCES ont_learned_fact(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    target_type  VARCHAR(20) NOT NULL,
    target_id    UUID NOT NULL,
    role         VARCHAR(30) NOT NULL DEFAULT 'about',
    note         TEXT,
    created_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_flink_fact ON ont_fact_link(fact_id);
CREATE INDEX IF NOT EXISTS idx_ont_flink_target ON ont_fact_link(target_type, target_id);

-- ==================== ont_20. ont_agent_annotation ====================
-- Stores per-question auto-tokenization results for agent-v2 chat.
-- status=false: pending annotation; status=true: confirmed, used as few-shot.
CREATE TABLE IF NOT EXISTS ont_agent_annotation (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    thread_id       UUID REFERENCES ont_agent_thread(id) ON DELETE SET NULL,
    question        TEXT NOT NULL,
    tokens          TEXT,           -- pipe-separated: "IPS5|15IWC11|接单"
    token_mappings  JSONB,          -- [{token,keyword,odName,propName,mappedTable,mappedField,tier}]
    status          BOOLEAN DEFAULT false,
    question_vector vector(1024),
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_ann_project  ON ont_agent_annotation (project_id);
CREATE INDEX IF NOT EXISTS idx_ont_ann_status   ON ont_agent_annotation (project_id, status);
CREATE INDEX IF NOT EXISTS idx_ont_ann_thread   ON ont_agent_annotation (thread_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ont_ann_project_question ON ont_agent_annotation (project_id, md5(question));

-- ==================== ont_knowledge_keyword ====================
CREATE TABLE IF NOT EXISTS ont_knowledge_keyword (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    knowledge_id UUID NOT NULL REFERENCES ont_knowledge(id) ON DELETE CASCADE,
    project_id   UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    keyword      TEXT NOT NULL,
    keyword_vector vector(1024),
    created_at   TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_kk_knowledge ON ont_knowledge_keyword(knowledge_id);
CREATE INDEX IF NOT EXISTS idx_ont_kk_project ON ont_knowledge_keyword(project_id);

-- ==================== pbit-lakehouse extensions ====================
-- Parallel-path PBIT→PostgreSQL lakehouse import. See .omc/plans/pbit-lakehouse-import.md

-- project: pointer to the per-project pg schema hosting lakehouse tables
ALTER TABLE project
  ADD COLUMN IF NOT EXISTS lakehouse_schema TEXT DEFAULT '';

-- ont_object_type: provenance tag (distinct from source_type which is a connector dispatch field)
ALTER TABLE ont_object_type
  ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT '';
COMMENT ON COLUMN ont_object_type.origin IS
  'Provenance tag: pbit-bootstrap | manual-upload | derived-view | "" (legacy). Distinct from source_type which is a connector dispatch field.';

-- Import audit log
CREATE TABLE IF NOT EXISTS ont_import_log (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    imported_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    source_type      TEXT NOT NULL,
    source_filename  TEXT,
    table_count      INT,
    row_count_total  BIGINT,
    status           TEXT NOT NULL CHECK (status IN ('pending','loading','success','failed','rolled_back','partial')),
    error_message    TEXT
);
CREATE INDEX IF NOT EXISTS idx_ont_import_log_project ON ont_import_log(project_id);

-- NEW-3: concurrent-import guard (unique partial index)
-- Second concurrent import for same project hits clean "already in progress" error
CREATE UNIQUE INDEX IF NOT EXISTS idx_ont_import_log_inprogress
    ON ont_import_log (project_id)
    WHERE status IN ('pending','loading');

-- Derived view registry (tracks Table.Combine / Binary.FromText / Table.Unpivot)
CREATE TABLE IF NOT EXISTS lakehouse_derived_view (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    pg_schema     TEXT NOT NULL,
    view_name     TEXT NOT NULL,
    m_expression  TEXT NOT NULL,
    base_tables   TEXT[] NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('combine','constant','unpivot','unsupported')),
    warning       TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, view_name)
);
CREATE INDEX IF NOT EXISTS idx_lakehouse_derived_view_project ON lakehouse_derived_view(project_id);

-- Per-table import progress tracking (supports resume after page close)
CREATE TABLE IF NOT EXISTS lakehouse_table_status (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    import_id      TEXT NOT NULL,
    table_name     TEXT NOT NULL,
    source_type    TEXT NOT NULL DEFAULT 'excel',
    partition_kind TEXT NOT NULL DEFAULT 'unsupported',
    status         TEXT NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','header_matched','loaded','skipped','error')),
    file_name      TEXT,
    row_count      INT,
    column_count   INT,
    matched_headers JSONB,
    error_message  TEXT,
    created_at     TIMESTAMPTZ DEFAULT now(),
    updated_at     TIMESTAMPTZ DEFAULT now(),
    UNIQUE (project_id, import_id, table_name)
);
CREATE INDEX IF NOT EXISTS idx_lts_project ON lakehouse_table_status(project_id);

-- Persist parsed PBIT config on the project for resume
ALTER TABLE project ADD COLUMN IF NOT EXISTS pbit_config JSONB DEFAULT NULL;

-- Lakehouse Ontology: SQL semantic layer fields on ont_object_type
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS semantic_sql TEXT DEFAULT '';
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS canonical_query TEXT DEFAULT '';
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS validated_at TIMESTAMPTZ;
COMMENT ON COLUMN ont_object_type.semantic_sql IS
  'Hand-written SQL semantic layer (e.g. SELECT * FROM proj_xxx."Table"). Used by lakehouse projects.';
COMMENT ON COLUMN ont_object_type.canonical_query IS
  'Solidified query: SELECT od."p1",od."p2" FROM (semantic_sql) AS od. Stored after validation.';
COMMENT ON COLUMN ont_object_type.validated_at IS
  'Timestamp of last successful SQL validation.';

-- Lakehouse SQL Engine: ont_metric SQL expression support
ALTER TABLE ont_metric ADD COLUMN IF NOT EXISTS sql_expression TEXT DEFAULT '';
COMMENT ON COLUMN ont_metric.sql_expression IS
  'Pre-defined SQL aggregate expression (e.g. SUM("Order_Quantity")). Lakehouse engine uses this for aggregation.';

-- Lakehouse SQL Engine: keyword vector for 3-tier correction
ALTER TABLE lakehouse_keyword ADD COLUMN IF NOT EXISTS keyword_vector vector(1024);
ALTER TABLE lakehouse_keyword ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT now();
ALTER TABLE lakehouse_keyword ADD COLUMN IF NOT EXISTS is_column_name BOOLEAN DEFAULT false;

-- Backfill: mark keywords matching their property name as column aliases
UPDATE lakehouse_keyword lk SET is_column_name = true
FROM ont_property p WHERE lk.property_id = p.id AND LOWER(lk.keyword) = LOWER(p.name)
AND lk.is_column_name = false;

-- Ontology SQL Passthrough: execution logs + saved snippets
CREATE TABLE IF NOT EXISTS ont_sql_passthrough_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id UUID,
  sql_text TEXT NOT NULL,
  mode VARCHAR(20) NOT NULL DEFAULT 'readonly',
  row_count INT NOT NULL DEFAULT 0,
  duration_ms INT NOT NULL DEFAULT 0,
  error TEXT DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sql_pass_log_project ON ont_sql_passthrough_log(project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS ont_sql_passthrough_snippet (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id UUID NOT NULL,
  name TEXT NOT NULL,
  sql_text TEXT NOT NULL,
  description TEXT DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(project_id, name)
);

-- Lakehouse SQL (direct query with pagination): logs + snippets
CREATE TABLE IF NOT EXISTS ont_lakehouse_sql_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id UUID,
  sql_text TEXT NOT NULL,
  row_count INT NOT NULL DEFAULT 0,
  duration_ms INT NOT NULL DEFAULT 0,
  error TEXT DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lh_sql_log_project ON ont_lakehouse_sql_log(project_id, created_at DESC);

CREATE TABLE IF NOT EXISTS ont_lakehouse_sql_snippet (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id UUID NOT NULL,
  name TEXT NOT NULL,
  sql_text TEXT NOT NULL,
  description TEXT DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(project_id, name)
);

-- ==================== Incremental Update (Git-style PBIX diff/apply) ====================
-- Soft delete + field-level blame for ontology entities.
-- user_edited_fields stores names of columns the user has manually edited
-- (e.g. {'canonical_query','description','mark'}). Incremental imports
-- must NOT overwrite any column listed in this array.
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS user_edited_fields TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE ont_property    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE ont_property    ADD COLUMN IF NOT EXISTS user_edited_fields TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE ont_property    ADD COLUMN IF NOT EXISTS short_description TEXT;
ALTER TABLE ont_link_type   ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE ont_link_type   ADD COLUMN IF NOT EXISTS user_edited_fields TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE ont_metric      ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE ont_metric      ADD COLUMN IF NOT EXISTS user_edited_fields TEXT[] NOT NULL DEFAULT '{}';

-- Orphan flag for associated user assets (never hard-deleted during update).
ALTER TABLE lakehouse_keyword ADD COLUMN IF NOT EXISTS orphan_at TIMESTAMPTZ;
ALTER TABLE ont_knowledge     ADD COLUMN IF NOT EXISTS orphan_at TIMESTAMPTZ;

-- Stash-like pending update plan; persists across page refresh.
-- One pending plan per project is the typical shape, but nothing prevents more.
CREATE TABLE IF NOT EXISTS ont_update_plan (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id        UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  source_file       TEXT,
  source_hash       TEXT,
  staging_dir       TEXT,                            -- path under /data/pbit-staging where parsed CSVs live
  proposed_snapshot JSONB NOT NULL,                  -- LakehouseSnapshot
  diff_summary      JSONB NOT NULL DEFAULT '[]'::jsonb,  -- []DiffItem
  selected_items    JSONB NOT NULL DEFAULT '[]'::jsonb,  -- []string  (diff item paths user has checked)
  status            VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending | applying | applied | dropped | failed
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  applied_at        TIMESTAMPTZ,
  error             TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_update_plan_project_status ON ont_update_plan(project_id, status);

-- Commit log (git-style): one row per apply of an update plan (incremental | revert).
-- Separate from ont_import_log (which tracks the import lock lifecycle for initial imports).
CREATE TABLE IF NOT EXISTS ont_update_commit (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id         UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  plan_id            UUID REFERENCES ont_update_plan(id) ON DELETE SET NULL,
  kind               VARCHAR(20) NOT NULL,  -- 'incremental' | 'revert'
  source_file        TEXT,
  before_snapshot    JSONB,                 -- minimal projection of affected entities before apply
  after_snapshot     JSONB,                 -- minimal projection of affected entities after  apply
  applied_diff       JSONB NOT NULL DEFAULT '[]'::jsonb,   -- []DiffItem actually executed
  summary            JSONB NOT NULL DEFAULT '{}'::jsonb,   -- {applied:N, skipped:M, errors:K}
  applied_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  reverted_at        TIMESTAMPTZ,
  reverted_by_commit_id UUID REFERENCES ont_update_commit(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_update_commit_project_applied ON ont_update_commit(project_id, applied_at DESC);
CREATE INDEX IF NOT EXISTS idx_update_commit_plan ON ont_update_commit(plan_id);

COMMENT ON COLUMN ont_object_type.user_edited_fields IS
  'Field-level blame: column names manually edited via /ontology/lakehouse-objects. Incremental imports skip these columns.';
COMMENT ON COLUMN lakehouse_keyword.orphan_at IS
  'Set to NOW() when the referenced property is soft-deleted by incremental update. User decides whether to keep or remove.';
COMMENT ON TABLE ont_update_plan IS
  'Stash for incremental PBIX update: holds proposed snapshot + diff until user applies or drops.';
COMMENT ON TABLE ont_update_commit IS
  'Commit log of incremental PBIX updates (git-style). applied_diff drives revert logic; distinct from ont_import_log which is the initial-import lock table.';

ALTER TABLE lakehouse_keyword ADD COLUMN IF NOT EXISTS aliases TEXT[] DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_lakehouse_keyword_aliases ON lakehouse_keyword USING GIN (aliases);

-- Per-alias vector child table. pgvector 0.8.0 has no vector(N)[] array column,
-- so each alias gets its own row. Synced with lakehouse_keyword.aliases by the
-- aliases PUT handler (DELETE removed, INSERT added with NULL vector). Tier 4
-- vectorMatch (correction.go) UNIONs these with lakehouse_keyword.keyword_vector
-- when looking up the nearest canonical keyword for fuzzy filter-value rewrite.
CREATE TABLE IF NOT EXISTS lakehouse_keyword_alias_vector (
    keyword_id   UUID NOT NULL REFERENCES lakehouse_keyword(id) ON DELETE CASCADE,
    alias        TEXT NOT NULL,
    alias_vector vector(1024),
    updated_at   TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (keyword_id, alias)
);
CREATE INDEX IF NOT EXISTS idx_lhkav_keyword ON lakehouse_keyword_alias_vector(keyword_id);

-- Vector similarity indexes (HNSW, cosine) for the Tier-4 fuzzy keyword recall
-- path: vector_topn.go / recall_lakehouse.go UNION lakehouse_keyword.keyword_vector
-- with lakehouse_keyword_alias_vector.alias_vector, then ORDER BY vec <=> $.
-- HNSW (unlike IVFFlat) builds fine on an empty table and needs no `lists`
-- tuning, so it is safe to create at schema-init time. Without these, those
-- ORDER BY <=> queries seq-scan and p99 grows linearly with embedding rows.
CREATE INDEX IF NOT EXISTS idx_lhk_keyword_vector
  ON lakehouse_keyword USING hnsw (keyword_vector vector_cosine_ops);
CREATE INDEX IF NOT EXISTS idx_lhkav_alias_vector
  ON lakehouse_keyword_alias_vector USING hnsw (alias_vector vector_cosine_ops);

-- One-shot + idempotent backfill: existing rows in lakehouse_keyword.aliases
-- predate the child table, so seed alias rows with NULL vector. The aliases
-- PUT handler keeps things synced going forward; the compute-vectors endpoint
-- also re-runs this self-heal step before each batch (defensive).
INSERT INTO lakehouse_keyword_alias_vector (keyword_id, alias, alias_vector)
SELECT lk.id, a, NULL
  FROM lakehouse_keyword lk
  CROSS JOIN LATERAL unnest(COALESCE(lk.aliases, '{}'::text[])) AS a
 WHERE lk.aliases IS NOT NULL AND array_length(lk.aliases, 1) > 0
ON CONFLICT (keyword_id, alias) DO NOTHING;

-- ==================== Lakehouse Test Run Versioning ====================
-- A "Run" is a versioned execution of a test suite, bound to a specific LLM config.
-- Each run snapshots questions from ont_test_case into ont_test_run_case and
-- preserves results independently, enabling side-by-side comparison across runs/models.

-- One row per versioned run of a lakehouse test suite.
CREATE TABLE IF NOT EXISTS ont_test_run (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    suite_id        UUID NOT NULL REFERENCES ont_test_suite(id) ON DELETE CASCADE,
    title           VARCHAR(200) NOT NULL,
    -- Nullable + SET NULL so deleting an llm_config blanks this FK without
    -- destroying the historical run. The snapshot columns below preserve
    -- vendor / model_name / alias as they were at run time.
    llm_config_id   UUID REFERENCES llm_config(id) ON DELETE SET NULL,
    llm_vendor      TEXT,                                      -- snapshot of llm_config.vendor at run time
    llm_model_name  TEXT,                                      -- snapshot of llm_config.model_name at run time
    llm_alias       TEXT,                                      -- snapshot of llm_config.alias at run time
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',  -- pending | running | completed | error | cancelled
    concurrency     INT NOT NULL DEFAULT 1,
    total           INT NOT NULL DEFAULT 0,
    completed_count INT NOT NULL DEFAULT 0,
    error_count     INT NOT NULL DEFAULT 0,
    is_default      BOOLEAN NOT NULL DEFAULT FALSE,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ont_test_run_suite ON ont_test_run(suite_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ont_test_run_default ON ont_test_run(suite_id) WHERE is_default = TRUE;
-- Cooperative cancellation flag — set by POST /lh-test-runs/{id}/cancel; the
-- background worker checks this between case completions and marks remaining
-- pending cases + the run itself as 'cancelled' once it sees true.
ALTER TABLE ont_test_run ADD COLUMN IF NOT EXISTS cancel_requested BOOLEAN DEFAULT FALSE NOT NULL;

-- Per-run result snapshot. One row per (run, case) pair.
CREATE TABLE IF NOT EXISTS ont_test_run_case (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id            UUID NOT NULL REFERENCES ont_test_run(id) ON DELETE CASCADE,
    case_id           UUID NOT NULL REFERENCES ont_test_case(id) ON DELETE CASCADE,
    sort_order        INT NOT NULL DEFAULT 0,
    code              VARCHAR(20) NOT NULL DEFAULT '',
    user_question     TEXT NOT NULL,
    status            VARCHAR(20) NOT NULL DEFAULT 'pending',
    function_calls    JSONB,
    final_answer      TEXT,
    generated_sql     TEXT DEFAULT '',
    execution_status  VARCHAR(20) DEFAULT '',
    execution_result  TEXT DEFAULT '',
    execution_error   TEXT DEFAULT '',
    duration_ms       INT DEFAULT 0,
    model_name        VARCHAR(100) DEFAULT '',
    prompt_tokens     INT DEFAULT 0,
    completion_tokens INT DEFAULT 0,
    total_tokens      INT DEFAULT 0,
    mark              VARCHAR(10),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(run_id, case_id)
);
CREATE INDEX IF NOT EXISTS idx_ont_test_run_case_run ON ont_test_run_case(run_id, sort_order);

-- ont_test_run_case: per-run-case annotation (added 2026-04-16 for dataset-testing revamp)
-- note = free-text reviewer comment (why this case is wrong, what the expected answer was, etc.)
-- mark widened to VARCHAR(20) to accept future labels (e.g. 'needs-review')
ALTER TABLE ont_test_run_case ADD COLUMN IF NOT EXISTS note TEXT;
ALTER TABLE ont_test_run_case ALTER COLUMN mark TYPE VARCHAR(20);

-- ==================== ont_test_case_tag (dictionary) ====================
-- Tags are suite-scoped (each dataset has its own tag namespace).
-- Case rows link via ont_test_case_tag_link (M2M). Deleting a tag cascades to links.
CREATE TABLE IF NOT EXISTS ont_test_case_tag (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    suite_id   UUID NOT NULL REFERENCES ont_test_suite(id) ON DELETE CASCADE,
    name       VARCHAR(100) NOT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (suite_id, name)
);
CREATE INDEX IF NOT EXISTS idx_ont_test_case_tag_suite ON ont_test_case_tag(suite_id, sort_order);

CREATE TABLE IF NOT EXISTS ont_test_case_tag_link (
    case_id UUID NOT NULL REFERENCES ont_test_case(id) ON DELETE CASCADE,
    tag_id  UUID NOT NULL REFERENCES ont_test_case_tag(id) ON DELETE CASCADE,
    PRIMARY KEY (case_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_ont_test_case_tag_link_tag ON ont_test_case_tag_link(tag_id);


-- ==================== mcp_api_key ====================
-- Phase 3 MCP: external-consumer API keys for mcp-tools-server. The
-- service never stores or logs raw keys; `key_hash` is the lowercase
-- hex SHA-256 of the raw key, computed in Go before any DB call.
-- Revocation is `UPDATE mcp_api_key SET revoked_at = now() WHERE id=$`
-- (no physical delete — keeps audit trail). Label is an operator-set
-- memo ("claude-code-laptop", "prod-python-script"). `last_used_at`
-- is touched best-effort on each successful auth so ops can spot
-- stale keys.
CREATE TABLE IF NOT EXISTS mcp_api_key (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash      TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL DEFAULT '',
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_mcp_api_key_active ON mcp_api_key(key_hash) WHERE revoked_at IS NULL;

-- Phase 3 MCP-3: optional per-key tool allow-list. NULL = all tools
-- allowed (admin convenience); empty array = explicit lockdown; list
-- of tool names = whitelist. Checked in services/mcp-tools-server/auth
-- after bearer-token validation.
ALTER TABLE mcp_api_key ADD COLUMN IF NOT EXISTS allowed_tools TEXT[];

-- 2026-04-25: per-user ownership for UI-minted keys. NULL = legacy/bootstrap.
ALTER TABLE mcp_api_key ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES "user"(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_mcp_api_key_user_active ON mcp_api_key(user_id) WHERE revoked_at IS NULL;

-- ==================== data_source ====================
-- Phase 3 collector: tracks Postgres / File / PBI data sources per project.
-- wizard_state JSONB holds in-progress wizard decisions (table roles, column
-- roles, link decisions) so the wizard can resume after a collector restart.
-- status='wizard_in_progress' + updated_at stale → SweepStaleOnBoot marks
-- 'failed_resumable' so the frontend can offer "Resume / Abandon".
CREATE TABLE IF NOT EXISTS data_source (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id      UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  type            TEXT NOT NULL CHECK (type IN ('postgres','file','pbi','sqlite')),
  label           TEXT NOT NULL,
  config_json     JSONB NOT NULL DEFAULT '{}'::jsonb,
  status          TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','connecting','ready','syncing','wizard_in_progress','completed','failed','failed_resumable')),
  staging_schema  TEXT,
  wizard_state    JSONB,
  created_by      UUID REFERENCES "user"(id),
  created_at      TIMESTAMPTZ DEFAULT now(),
  updated_at      TIMESTAMPTZ DEFAULT now(),
  last_sync_at    TIMESTAMPTZ,
  -- sha256 (hex) of the uploaded source bytes (PBIX / file connectors). Lets us
  -- reject re-uploading byte-identical content into the same project (409);
  -- different content coexists as a new data_source. NULL for sources with no
  -- uploaded artifact (postgres/sqlite live connectors).
  content_hash    TEXT
);

CREATE INDEX IF NOT EXISTS idx_data_source_project ON data_source(project_id);
CREATE INDEX IF NOT EXISTS idx_data_source_status_updated ON data_source(status, updated_at)
  WHERE status IN ('wizard_in_progress', 'syncing');
-- Fast "does this project already have a source with identical content?" probe.
CREATE INDEX IF NOT EXISTS idx_data_source_project_hash ON data_source(project_id, content_hash)
  WHERE content_hash IS NOT NULL;

-- data_source_id on ont_object_type: deferred to here (instead of next to the
-- other ont_object_type ALTERs ~650 lines above) because the FK references
-- data_source, which is created just above. Keeping it here is what lets a fresh
-- schema init run to completion under psql ON_ERROR_STOP=1. NULL = manual/builder
-- -created or legacy; ON DELETE SET NULL keeps objects when a source is removed.
ALTER TABLE ont_object_type ADD COLUMN IF NOT EXISTS data_source_id UUID
  REFERENCES data_source(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_ont_object_type_data_source
  ON ont_object_type(data_source_id) WHERE data_source_id IS NOT NULL;

-- ==================== ingest_job ====================
-- Durable background-task queue for collector-server. Workers claim jobs via
-- FOR UPDATE SKIP LOCKED, write heartbeat_at every 5s; sweeper marks stale
-- (>2min) running jobs as failed so a restarted container can recover.
CREATE TABLE IF NOT EXISTS ingest_job (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  data_source_id   UUID REFERENCES data_source(id) ON DELETE CASCADE,
  project_id       UUID NOT NULL,
  kind             TEXT NOT NULL CHECK (kind IN
                     ('file_upload','postgres_sync','pbit_ingest','pbix_extract','wizard_confirm')),
  status           TEXT NOT NULL DEFAULT 'queued' CHECK (status IN
                     ('queued','running','succeeded','failed','cancelled')),
  phase            TEXT,
  percent          SMALLINT DEFAULT 0,
  rows_done        BIGINT DEFAULT 0,
  rows_total       BIGINT DEFAULT 0,
  bytes_done       BIGINT DEFAULT 0,
  message          TEXT,
  worker_id        TEXT,
  heartbeat_at     TIMESTAMPTZ,
  started_at       TIMESTAMPTZ,
  completed_at     TIMESTAMPTZ,
  error            TEXT,
  retry_count      SMALLINT DEFAULT 0,
  payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
  cancel_requested BOOLEAN NOT NULL DEFAULT false,
  created_at       TIMESTAMPTZ DEFAULT now(),
  created_by       UUID REFERENCES "user"(id)
);

CREATE INDEX IF NOT EXISTS idx_ingest_job_pickup
  ON ingest_job(status, heartbeat_at)
  WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_ingest_job_proj
  ON ingest_job(project_id, created_at DESC);

-- Forward-migration: the inline CHECK above only applies on a fresh CREATE.
-- Existing DBs need the kind list widened to include 'pbix_extract'
-- (collector-server pbix import job). Drop-then-add is idempotent on re-run.
ALTER TABLE ingest_job DROP CONSTRAINT IF EXISTS ingest_job_kind_check;
ALTER TABLE ingest_job ADD CONSTRAINT ingest_job_kind_check
  CHECK (kind IN ('file_upload','postgres_sync','pbit_ingest','pbix_extract','wizard_confirm'));
CREATE INDEX IF NOT EXISTS idx_ingest_job_ds
  ON ingest_job(data_source_id, created_at DESC)
  WHERE data_source_id IS NOT NULL;

-- ==================== lakehouse_shape_capability ====================
-- Data-driven vocabulary for the mission reachability gate
-- (services/agent-server/handler/reachability_judge.go). The gate's LLM
-- decomposes each sub-question into the "shape" it requires (single-month
-- prefix, year range, exact match, …) and then needs to verify that some
-- cited Intent parameter actually declares the matching shape. This table
-- holds the *names* of recognised shapes plus the LLM-facing descriptions
-- that train it to classify correctly. Operators add / edit / remove rows
-- here — no Go or TS source ever names a specific shape.
--
-- Soft reference: lakehouse_metric_intent.parameters[*].shapeCapability
-- (text in JSONB) names a row in this table. JSONB cannot enforce the FK
-- itself; validation is the CRUD path's job (or none, since empty / missing
-- is the safe degradation).
--
-- Empty table = decompose gate falls back to its prior behaviour
-- (LLM-only judgement, no deterministic shape check) — so an unpopulated
-- table is safe.
--
-- `satisfies` is the subsumption list: a parameter declaring shape X can
-- ALSO serve a requirement the LLM classified as any name in X.satisfies
-- (plus X itself). Direction is strictly broader→narrower: a range shape
-- lists the point shape it subsumes (e.g. multi_period_range satisfies
-- single_period_prefix, because a true start/end range param can also
-- answer a single-period question). NEVER list a broader shape — doing so
-- re-opens the false-acceptance the gate exists to prevent. Empty = the
-- shape satisfies only itself (exact-match semantics). This lets the LLM
-- pick a narrower-but-compatible label without the gate falsely refusing.
--
-- Seed: docs/schema/seeds/shape_capability.sql.
CREATE TABLE IF NOT EXISTS lakehouse_shape_capability (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    examples    TEXT[] DEFAULT '{}',
    satisfies   TEXT[] DEFAULT '{}',
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
-- Forward-migration for existing DBs (CREATE TABLE IF NOT EXISTS is a no-op).
ALTER TABLE lakehouse_shape_capability ADD COLUMN IF NOT EXISTS satisfies TEXT[] DEFAULT '{}';

-- ==================== ont_audit_log ====================
-- Durable sink for authmw audit events (who did what on /internal + /api calls).
-- Replaces the stderr-only writer so the audit trail survives container restarts
-- and `docker logs` rotation. Identifier columns are TEXT so an unexpected value
-- can never abort the insert (and silently force the stderr fallback) on a cast.
-- RETENTION: this table grows unbounded — operators must prune or partition on
-- `ts` per their policy, e.g. `DELETE FROM ont_audit_log WHERE ts < now() - interval '90 days'`.
-- Every service's per-service DB role needs INSERT here (see ops/db-roles.sql).
CREATE TABLE IF NOT EXISTS ont_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id     TEXT,
    caller_service TEXT,
    on_behalf_of   TEXT,
    project_id     TEXT,
    path           TEXT,
    method         TEXT,
    status_code    INT
);
CREATE INDEX IF NOT EXISTS idx_ont_audit_log_ts ON ont_audit_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_ont_audit_log_project ON ont_audit_log(project_id) WHERE project_id <> '';
