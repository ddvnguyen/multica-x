-- M-332: the ask/plan escalation ladder.
--
-- The timeout no longer cancels the park: at the deadline the evidence row is
-- marked escalated_at (once — the sweep's CAS is `escalated_at IS NULL`) and
-- the project lead is triggered. The card/plan stays open, the parked task
-- stays 'blocked', and the Owner's answer still resumes the run. A lead that
-- answers on the Owner's behalf is recorded as an agent, not laundered into
-- the Owner's own answer.
ALTER TABLE agent_question_card ADD COLUMN escalated_at TIMESTAMPTZ;
ALTER TABLE agent_question_card ADD COLUMN answered_by_type TEXT NOT NULL DEFAULT 'member';
ALTER TABLE agent_plan ADD COLUMN escalated_at TIMESTAMPTZ;
