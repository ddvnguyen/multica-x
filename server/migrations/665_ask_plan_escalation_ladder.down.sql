ALTER TABLE agent_question_card DROP COLUMN IF EXISTS answered_by_type;
ALTER TABLE agent_question_card DROP COLUMN IF EXISTS escalated_at;
ALTER TABLE agent_plan DROP COLUMN IF EXISTS escalated_at;
