-- The workspace aggregate reads every mark in a workspace newest-first when it
-- lists recent votes, and only by workspace for the medians.
CREATE INDEX CONCURRENTLY IF NOT EXISTS task_memory_mark_workspace_created_idx
    ON task_memory_mark (workspace_id, created_at DESC);
