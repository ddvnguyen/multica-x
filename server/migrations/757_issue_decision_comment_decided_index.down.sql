-- The legacy index allowed only one record per comment. If newer data has
-- several decisions for one comment, retain the oldest before restoring it.
DELETE FROM issue_decision_record newer
USING issue_decision_record older
WHERE newer.comment_id = older.comment_id
  AND newer.id <> older.id
  AND (newer.created_at, newer.id) > (older.created_at, older.id);
DROP INDEX CONCURRENTLY IF EXISTS issue_decision_record_comment_decided_idx;
