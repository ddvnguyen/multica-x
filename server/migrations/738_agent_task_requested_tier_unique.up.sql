-- Keep preset fallback tasks in the pending slot of the requested tier. The
-- actual tier remains in tier_id; requested_tier_id lives in task context.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_one_pending_task_per_issue_agent_requested_tier
    ON agent_task_queue (
        issue_id,
        agent_id,
        COALESCE((context->>'requested_tier_id')::uuid, CASE WHEN tier_explicit THEN tier_id END, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid)
    )
    WHERE status IN ('queued', 'dispatched')
       OR (status = 'deferred' AND context->>'channel_issue_media_pending' = 'true');
