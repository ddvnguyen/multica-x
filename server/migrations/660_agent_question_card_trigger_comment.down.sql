DROP INDEX IF EXISTS idx_aqc_trigger_comment;
ALTER TABLE agent_question_card DROP COLUMN IF EXISTS trigger_comment_id;
