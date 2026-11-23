ALTER TABLE issue_decision_record
    ADD COLUMN problem TEXT NOT NULL DEFAULT '',
    ADD COLUMN problem_comment_id UUID,
    ADD COLUMN scope TEXT NOT NULL DEFAULT 'issue' CHECK (scope IN ('issue', 'project')),
    ADD COLUMN project_id UUID;

ALTER TABLE issue_decision_record
    DROP CONSTRAINT issue_decision_record_source_kind_check,
    ADD CONSTRAINT issue_decision_record_source_kind_check
        CHECK (source_kind IN ('comment', 'ask_answer', 'plan_approval', 'agent_recorded'));
