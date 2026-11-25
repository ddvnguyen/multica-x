CREATE INDEX CONCURRENTLY issue_decision_record_issue_idx
    ON issue_decision_record (workspace_id, issue_id, created_at DESC);
