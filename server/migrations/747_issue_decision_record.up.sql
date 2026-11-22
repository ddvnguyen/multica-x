CREATE TABLE issue_decision_record (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    comment_id UUID NOT NULL,
    decided TEXT NOT NULL,
    rationale TEXT NOT NULL DEFAULT '',
    supersedes_id UUID REFERENCES issue_decision_record(id) DEFERRABLE INITIALLY DEFERRED,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('comment', 'ask_answer', 'plan_approval')),
    actor_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
