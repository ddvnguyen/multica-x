-- Recreate the thread-scoped guard before 695's down step removes the
-- tier-scoped one. Do not use IF NOT EXISTS here: a leftover INVALID v3
-- must fail closed instead of being recorded as restored.
CREATE UNIQUE INDEX CONCURRENTLY idx_one_pending_task_per_issue_agent_thread
    ON agent_task_queue (issue_id, agent_id, COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status IN ('queued', 'dispatched')
       OR (status = 'deferred' AND context->>'channel_issue_media_pending' = 'true');
