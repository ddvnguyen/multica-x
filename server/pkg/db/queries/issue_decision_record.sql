-- name: CreateIssueDecisionRecord :exec
INSERT INTO issue_decision_record (
    id, workspace_id, issue_id, comment_id, decided, rationale, supersedes_id, source_kind, actor_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (comment_id) DO NOTHING
;

-- name: ListIssueDecisionRecords :many
SELECT r.*, replacement.id AS superseded_by_id
FROM issue_decision_record r
LEFT JOIN issue_decision_record replacement ON replacement.supersedes_id = r.id
WHERE r.workspace_id = $1 AND r.issue_id = $2
ORDER BY r.created_at ASC, r.id ASC;

-- name: ListCurrentIssueDecisionRecords :many
WITH current_records AS (
    SELECT r.*
    FROM issue_decision_record r
    WHERE r.workspace_id = $1 AND r.issue_id = $2
      AND NOT EXISTS (
          SELECT 1 FROM issue_decision_record replacement
          WHERE replacement.supersedes_id = r.id
      )
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT 10
)
SELECT r.*, NULL::uuid AS superseded_by_id
FROM current_records r
ORDER BY r.created_at ASC, r.id ASC;

-- name: IssueDecisionRecordExists :one
SELECT EXISTS (
    SELECT 1 FROM issue_decision_record r
    WHERE r.id = $1 AND r.workspace_id = $2 AND r.issue_id = $3
      AND NOT EXISTS (
          SELECT 1 FROM issue_decision_record replacement
          WHERE replacement.supersedes_id = r.id
      )
);
