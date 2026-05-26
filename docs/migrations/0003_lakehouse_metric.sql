-- 0003_lakehouse_metric.sql
--
-- Unified "指标" table. Folds 度量(ont_metric)/指标/意图(lakehouse_metric_intent)
-- into ONE concept: an Od-grounded, parameterized, data-returning query definition
-- (pipeline metric → od → ontologysql → lakehousesql). It COEXISTS with
-- lakehouse_metric_intent during the compatibility window — this migration is purely
-- additive and does NOT touch the old table. Data migration + cutover + deprecation
-- is a later phase. canonical_metric stays NOT NULL (aggregation mandatory). The
-- flat pivot_* columns mirror lakehouse_metric_intent so the recall renderer + the
-- SmartQuery executor are reused unchanged via an adapter. Idempotent.

CREATE TABLE IF NOT EXISTS lakehouse_metric (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id               UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    object_id                UUID NOT NULL REFERENCES ont_object_type(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,
    display_name             TEXT,
    description              TEXT,
    level                    TEXT NOT NULL DEFAULT 'simple',   -- 'simple' | 'plan'
    canonical_metric         TEXT NOT NULL,
    canonical_filters        JSONB NOT NULL DEFAULT '[]'::jsonb,
    auto_group_by            TEXT[] NOT NULL DEFAULT '{}',
    replace_group_by         BOOLEAN NOT NULL DEFAULT false,
    default_order_by_label   TEXT,
    default_order_by_dir     TEXT,
    default_limit            INT,
    pivot_on                 TEXT,
    pivot_values             TEXT[],
    pivot_column_labels      TEXT[],
    pivot_total_label        TEXT DEFAULT 'Total',
    pivot_with_percent       BOOLEAN DEFAULT FALSE,
    pivot_append_grand_total BOOLEAN DEFAULT FALSE,
    pivot_percent_axis       TEXT DEFAULT 'row',
    pivot_percent_scope      TEXT NOT NULL DEFAULT 'filtered',
    pivot_percent_suffix     TEXT DEFAULT '占比',
    parameters               JSONB NOT NULL DEFAULT '[]'::jsonb,
    plan                     JSONB,
    response_template        TEXT,
    priority                 INT NOT NULL DEFAULT 0,
    mark                     BOOLEAN NOT NULL DEFAULT TRUE,
    status                   TEXT NOT NULL DEFAULT 'active',
    schema_version           INT NOT NULL DEFAULT 1,
    definition               JSONB NOT NULL DEFAULT '{}'::jsonb,
    extra                    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by               UUID REFERENCES "user"(id),
    created_at               TIMESTAMPTZ DEFAULT now(),
    updated_at               TIMESTAMPTZ DEFAULT now(),
    deleted_at               TIMESTAMPTZ,
    UNIQUE (project_id, name)
);

DO $$ BEGIN
    ALTER TABLE lakehouse_metric
        ADD CONSTRAINT lakehouse_metric_default_order_by_dir_chk
        CHECK (default_order_by_dir IS NULL OR default_order_by_dir IN ('ASC','DESC'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS idx_lh_metric_project ON lakehouse_metric(project_id);
CREATE INDEX IF NOT EXISTS idx_lh_metric_object  ON lakehouse_metric(object_id);

-- Trigger-keyword link to the unified metric (mirrors metric_intent_id).
ALTER TABLE lakehouse_keyword
    ADD COLUMN IF NOT EXISTS metric_id UUID;
DO $$ BEGIN
    ALTER TABLE lakehouse_keyword
        ADD CONSTRAINT lakehouse_keyword_metric_id_fkey
        FOREIGN KEY (metric_id)
        REFERENCES lakehouse_metric(id) ON DELETE CASCADE;
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Relax the keyword anchor CHECK to also accept a metric_id anchor.
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
