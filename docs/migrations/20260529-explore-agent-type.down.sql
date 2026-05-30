-- 20260529-explore-agent-type.down.sql
--
-- Rollback for 20260529-explore-agent-type.sql. Removes 'explore' from the
-- allowed agent_type values. Deletes any orphaned explore rows first so the
-- narrowed CHECK does not fail on existing data.

DELETE FROM ont_agent_thread WHERE agent_type = 'explore';
ALTER TABLE ont_agent_thread DROP CONSTRAINT IF EXISTS agent_type_chk;
ALTER TABLE ont_agent_thread
  ADD CONSTRAINT agent_type_chk
  CHECK (agent_type IN ('lakehouse','builder'));
