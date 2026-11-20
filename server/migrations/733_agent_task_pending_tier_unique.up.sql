-- M-359: tier-scoped pending-task uniqueness. An explicit `@agent#tier-a`
-- + `@agent#tier-b` pair in one comment must run as TWO sibling tasks, so
-- the pending slot is per (issue, agent, tier): plain (tier-less) triggers
-- collapse tier_id to the zero UUID and keep the exact v3/thread guard,
-- tier-stamped triggers each get their own slot. Build before dropping the
-- thread-scoped guard (migration 696), so uniqueness stays enforced
-- throughout rolling deploys. Single statement: CREATE INDEX CONCURRENTLY
-- cannot run in a transaction.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_one_pending_task_per_issue_agent_tier
    ON agent_task_queue (issue_id, agent_id, COALESCE(CASE WHEN tier_explicit THEN tier_id END, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(comment_thread_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status IN ('queued', 'dispatched')
       OR (status = 'deferred' AND context->>'channel_issue_media_pending' = 'true');
