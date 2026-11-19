-- Retire the thread-scoped pending guard after the tier-scoped guard
-- (migration 695) is available.
DROP INDEX CONCURRENTLY IF EXISTS idx_one_pending_task_per_issue_agent_thread;
