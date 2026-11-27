-- Marks are observations of one run's memory comparison; dropping the table
-- loses the votes and nothing any request path reads.
DROP INDEX IF EXISTS task_memory_mark_workspace_created_idx;
DROP TABLE IF EXISTS task_memory_mark;
