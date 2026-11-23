CREATE UNIQUE INDEX CONCURRENTLY issue_decision_record_comment_decided_idx
    ON issue_decision_record (comment_id, md5(decided));
