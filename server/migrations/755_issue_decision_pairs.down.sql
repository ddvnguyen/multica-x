ALTER TABLE issue_decision_record
    DROP COLUMN IF EXISTS project_id,
    DROP COLUMN IF EXISTS scope,
    DROP COLUMN IF EXISTS problem_comment_id,
    DROP COLUMN IF EXISTS problem;

UPDATE issue_decision_record SET source_kind = 'comment' WHERE source_kind = 'agent_recorded';

ALTER TABLE issue_decision_record
    DROP CONSTRAINT IF EXISTS issue_decision_record_source_kind_check,
    ADD CONSTRAINT issue_decision_record_source_kind_check
        CHECK (source_kind IN ('comment', 'ask_answer', 'plan_approval'));
