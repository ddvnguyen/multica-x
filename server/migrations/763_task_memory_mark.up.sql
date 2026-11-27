-- Memory compare marks (M-616): one judgement per run on which pre-run memory
-- block was more useful — the injected MOCT one, the PowerContext shadow read
-- beside it on the same claim, or neither.
--
-- The numbers the voter saw are copied onto the row rather than joined from the
-- run at read time: a shadow block is a measurement of one claim, and a median
-- over later runs must not silently re-price an older vote. Rows are written
-- only from the run detail panel, so a workspace that never compares anything
-- never writes here.
CREATE TABLE IF NOT EXISTS task_memory_mark (
    task_id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    -- choice is the vote itself; 'neither' is a real answer, not an abstention.
    choice text NOT NULL CHECK (choice IN ('moct', 'powercontext', 'neither')),
    -- marked_by_type is 'member' or 'agent' (resolveActor), so an agent that
    -- voted for its own run can be told apart from a person's judgement.
    marked_by_type text NOT NULL,
    marked_by_id uuid NOT NULL,
    primary_bytes integer NOT NULL DEFAULT 0,
    primary_latency_ms integer NOT NULL DEFAULT 0,
    shadow_bytes integer NOT NULL DEFAULT 0,
    shadow_latency_ms integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
