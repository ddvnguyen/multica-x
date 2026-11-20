CREATE INDEX CONCURRENTLY IF NOT EXISTS question_group_idx
    ON question (group_id);
