-- Structured agent questions.
--
-- A runtime tool call that needs a decision from a person (or a leader agent)
-- becomes a row here instead of blocking invisibly inside the CLI. One
-- AskUserQuestion tool call can carry several questions, so `group_id` ties
-- the rows of one call together and `position` preserves the model's order;
-- the daemon answers the whole group in a single control_response.
--
-- The schema is deliberately runtime-neutral: nothing here names CodeBuddy.
-- The claude / omp / pi slices reuse the same rows and only differ in how the
-- backend parks the tool call.
CREATE TABLE question (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    issue_id UUID REFERENCES issue(id) ON DELETE CASCADE,
    task_id UUID REFERENCES agent_task_queue(id) ON DELETE CASCADE,
    -- One row per question; group_id is the tool call that asked them.
    group_id UUID NOT NULL,
    position INT NOT NULL DEFAULT 0,
    tool_call_id TEXT NOT NULL DEFAULT '',
    asked_by_agent_id UUID,
    -- Who is being asked. NULL addressee means "anyone in the workspace can
    -- answer", which is what a question with no resolvable owner falls back
    -- to. `member` and `agent` are the only identities the platform has.
    addressee_type TEXT CHECK (addressee_type IN ('member', 'agent')),
    addressee_id UUID,
    header TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL,
    -- [{label, description}] — kept as JSON because the labels are the
    -- provider's own and the answer is stored as the chosen label verbatim.
    options JSONB NOT NULL DEFAULT '[]',
    multi_select BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'expired')),
    answer TEXT,
    -- Attribution is required for an answered row: an agent answering on the
    -- owner's behalf must stay distinguishable from the owner answering.
    answered_by_type TEXT CHECK (answered_by_type IN ('member', 'agent')),
    answered_by_id UUID,
    answered_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status <> 'answered' OR (answer IS NOT NULL AND answered_by_type IS NOT NULL))
);

-- The daemon polls one tool call's rows; the issue view lists one issue's.
CREATE INDEX question_group_idx ON question (group_id);
CREATE INDEX question_task_status_idx ON question (task_id, status);
CREATE INDEX question_issue_status_idx ON question (issue_id, status, created_at DESC);
