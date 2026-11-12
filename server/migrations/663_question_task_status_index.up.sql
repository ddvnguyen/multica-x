CREATE INDEX CONCURRENTLY IF NOT EXISTS question_task_status_idx
    ON question (task_id, status);
