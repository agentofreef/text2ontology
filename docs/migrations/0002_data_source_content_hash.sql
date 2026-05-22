-- 0002_data_source_content_hash
--
-- Adds data_source.content_hash (sha256 hex of the uploaded source bytes) plus a
-- partial index, enabling same-content dedup within a project.
--
-- Context: a project may hold MANY data sources (multiple PBIX, file, postgres,
-- sqlite). To make re-uploads sane we reject byte-identical re-uploads into the
-- same project (HTTP 409) while letting genuinely different content coexist as a
-- new source. Fresh installs get this column from docs/schema/schema.sql; this
-- migration brings existing databases in line.
--
-- Idempotent: safe to re-run (IF NOT EXISTS guards). One transaction per file.

ALTER TABLE data_source ADD COLUMN IF NOT EXISTS content_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_data_source_project_hash
  ON data_source(project_id, content_hash)
  WHERE content_hash IS NOT NULL;
