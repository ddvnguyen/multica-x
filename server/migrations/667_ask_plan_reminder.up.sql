-- M-362: the mid-timeout reminder stamp.
--
-- The sweeper fires one reminder bump at ~50% of the effective timeout while
-- the evidence is still open. The stamp is the idempotency gate (same CAS
-- shape as escalated_at): a stamped row never reminds twice. Nullable: rows
-- predating this migration predate their reminder window, so they never
-- remind — only parks created after it can.
ALTER TABLE agent_question_card ADD COLUMN reminded_at TIMESTAMPTZ;
ALTER TABLE agent_plan ADD COLUMN reminded_at TIMESTAMPTZ;
