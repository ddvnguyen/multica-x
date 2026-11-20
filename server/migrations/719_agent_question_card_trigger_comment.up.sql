-- M-285: bind an ask card to the comment thread that triggered its run.
ALTER TABLE agent_question_card ADD COLUMN trigger_comment_id UUID;
CREATE INDEX idx_aqc_trigger_comment ON agent_question_card (trigger_comment_id) WHERE trigger_comment_id IS NOT NULL;
