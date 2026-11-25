CREATE UNIQUE INDEX CONCURRENTLY issue_decision_record_supersedes_idx
    ON issue_decision_record (supersedes_id)
    WHERE supersedes_id IS NOT NULL;
