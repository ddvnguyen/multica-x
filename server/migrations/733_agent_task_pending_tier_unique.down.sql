-- Down of 695: drop the tier-scoped guard. The thread-scoped guard is
-- restored by 696's down step.
DROP INDEX CONCURRENTLY IF EXISTS idx_one_pending_task_per_issue_agent_tier;
