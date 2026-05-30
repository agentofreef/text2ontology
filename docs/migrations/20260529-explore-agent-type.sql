-- 20260529-explore-agent-type.sql
--
-- Widen ont_agent_thread.agent_type CHECK to accept 'explore' as a third
-- agent type alongside 'lakehouse' and 'builder'. Enables the chat-first
-- explore mode (.omc/plans/plan-explore-chat-redesign-final.md, AC-3).
-- Idempotent (DROP CONSTRAINT IF EXISTS then ADD).

ALTER TABLE ont_agent_thread DROP CONSTRAINT IF EXISTS agent_type_chk;
ALTER TABLE ont_agent_thread
  ADD CONSTRAINT agent_type_chk
  CHECK (agent_type IN ('lakehouse','builder','explore'));
