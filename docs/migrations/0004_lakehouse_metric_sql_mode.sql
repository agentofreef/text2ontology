-- 0004_lakehouse_metric_sql_mode.sql
--
-- Adds "SQL mode" to the unified metric (lakehouse_metric). A metric can now be
-- authored two ways:
--   * level='simple'|'plan' — structured fields compiled by the SmartQuery engine
--     (the existing behavior).
--   * level='sql'           — a HUMAN-authored parameterized SQL (written against
--     ontology Od names, with {{paramName}} placeholders) stored in query_sql; at
--     runtime the placeholders bind as positional $N driver args (injection-safe)
--     and the SQL executes read-only. canonical_metric carries the sentinel '(sql)'
--     for these rows (mirrors the '(plan)' sentinel), so the NOT NULL constraint
--     stays and the structured path simply isn't taken (routing keys on level).
--
-- Purely additive. Idempotent.

ALTER TABLE lakehouse_metric
    ADD COLUMN IF NOT EXISTS query_sql TEXT;
