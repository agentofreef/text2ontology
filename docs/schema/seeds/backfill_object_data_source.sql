-- backfill_object_data_source.sql
--
-- One-time, BEST-EFFORT backfill of ont_object_type.data_source_id for objects
-- created before that column existed. Run manually (psql -f ...) AFTER applying
-- the data_source_id ALTER in docs/schema/schema.sql. It is idempotent and only
-- ever touches rows where data_source_id IS NULL, so re-running is safe.
--
-- Heuristic: match an object to a data_source by comparing the object's
-- source_table (stored as "<schema>.<table>") against the data_source's
-- staging_schema, scoped to the same project. Unmatched rows are LEFT NULL on
-- purpose — the data-architecture view buckets NULL + file-type sources into a
-- shared "CSV folder" node, so a NULL is a valid, harmless state.
--
-- CAVEAT (read before trusting the result): in the wizard import path, staged
-- data lands in `collector_<dsid>` (data_source.staging_schema) but is then
-- merged into the project's FINAL lakehouse schema `proj_<hex>` before objects
-- are authored, so most objects' source_table references `proj_<hex>.<table>`,
-- NOT the staging_schema. The match below will therefore only catch objects
-- whose source_table still references the staging schema. Inspect your actual
-- source_table values first:
--
--   SELECT DISTINCT split_part(source_table, '.', 1) AS schema_prefix
--   FROM ont_object_type WHERE source_table IS NOT NULL;
--
-- If your objects use `proj_<hex>` prefixes (the common case), this query
-- legitimately matches zero rows and the backfill is a no-op — that is expected,
-- not a bug. Adjust the LIKE clause to your real source_table format if needed.

UPDATE ont_object_type o
SET data_source_id = d.id
FROM data_source d
WHERE o.data_source_id IS NULL
  AND d.staging_schema IS NOT NULL
  AND d.staging_schema <> ''
  AND o.source_table LIKE d.staging_schema || '.%'
  AND o.project_id = d.project_id;
