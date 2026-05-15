-- audit-builder-products.sql
--
-- PURPOSE: surfaces builder-origin ontology rows that are currently mark=true
-- but have no validated_at timestamp. These rows were silently activated by the
-- propose_* bug (INSERT mark=true instead of mark=false) that existed before
-- the fix in tool_builder_propose.go. They are active in recall/smartquery but
-- were never intentionally "启用" by a user.
--
-- THIS SCRIPT IS READ-ONLY. It does not modify any data.
--
-- HOW TO USE:
--   psql "$DATABASE_URL" -f scripts/audit-builder-products.sql
--
-- OUTPUT: one row per affected entity, ordered by table then creation date.
-- Review the results and decide manually which rows (if any) to flip back to
-- mark=false. The fix only changes behavior going forward; existing prod data
-- is left untouched by the migration.
--
-- EXAMPLE remediation (run only after manual review):
--   UPDATE ont_object_type SET mark=false WHERE id='<uuid>' AND project_id='<uuid>';
--   UPDATE lakehouse_metric_intent SET mark=false WHERE id='<uuid>' AND project_id='<uuid>';
--   UPDATE ont_link_type SET mark=false WHERE id='<uuid>' AND project_id='<uuid>';

SELECT
    'ont_object_type'     AS tbl,
    id,
    project_id,
    name,
    source_type           AS source_type_or_kind,
    validated_at,
    created_at
FROM ont_object_type
WHERE origin = 'builder'
  AND mark = true
  AND validated_at IS NULL

UNION ALL

SELECT
    'ont_property'        AS tbl,
    p.id,
    p.project_id,
    p.name,
    p.data_type           AS source_type_or_kind,
    NULL::timestamptz     AS validated_at,
    p.created_at
FROM ont_property p
JOIN ont_object_type o ON o.id = p.object_type_id
WHERE o.origin = 'builder'
  AND p.mark = true

UNION ALL

SELECT
    'lakehouse_metric_intent' AS tbl,
    i.id,
    i.project_id,
    i.name,
    'intent'              AS source_type_or_kind,
    NULL::timestamptz     AS validated_at,
    i.created_at
FROM lakehouse_metric_intent i
JOIN ont_object_type o ON o.id = i.object_id
WHERE o.origin = 'builder'
  AND i.mark = true

UNION ALL

SELECT
    'ont_link_type'       AS tbl,
    l.id,
    l.project_id,
    l.link_name           AS name,
    l.cardinality         AS source_type_or_kind,
    NULL::timestamptz     AS validated_at,
    l.created_at
FROM ont_link_type l
JOIN ont_object_type o ON o.id = l.from_object_id
WHERE o.origin = 'builder'
  AND l.mark = true

ORDER BY tbl, created_at DESC;
