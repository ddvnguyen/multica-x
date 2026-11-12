CREATE INDEX CONCURRENTLY IF NOT EXISTS question_issue_status_idx
    ON question (issue_id, status, created_at DESC);
