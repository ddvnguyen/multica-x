-- name: CreateIssueDecisionRecord :exec
INSERT INTO issue_decision_record (
    id, workspace_id, issue_id, comment_id, problem, problem_comment_id,
    decided, rationale, supersedes_id, source_kind, actor_id, scope, project_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (comment_id, (md5(decided))) DO NOTHING
;

-- name: GetIssueDecisionRecordByCommentAndDecided :one
SELECT * FROM issue_decision_record
WHERE comment_id = $1 AND decided = $2;

-- name: ListIssueDecisionRecords :many
SELECT r.id, r.workspace_id, r.issue_id, r.comment_id, r.decided, r.rationale,
       r.supersedes_id, r.source_kind, r.actor_id, r.created_at, replacement.id AS superseded_by_id,
       r.problem, r.problem_comment_id, r.scope, r.project_id
FROM issue_decision_record r
LEFT JOIN issue_decision_record replacement ON replacement.supersedes_id = r.id
WHERE r.workspace_id = $1 AND r.issue_id = $2
ORDER BY r.created_at ASC, r.id ASC;

-- name: ListCurrentIssueDecisionRecords :many
WITH current_records AS (
    SELECT r.id, r.workspace_id, r.issue_id, r.comment_id, r.decided, r.rationale,
           r.supersedes_id, r.source_kind, r.actor_id, r.created_at, r.problem,
           r.problem_comment_id, r.scope, r.project_id
    FROM issue_decision_record r
    WHERE r.workspace_id = $1 AND r.issue_id = $2
      AND r.scope = 'issue'
      AND NOT EXISTS (
          SELECT 1 FROM issue_decision_record replacement
          WHERE replacement.supersedes_id = r.id
      )
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT 10
)
SELECT r.id, r.workspace_id, r.issue_id, r.comment_id, r.decided, r.rationale,
       r.supersedes_id, r.source_kind, r.actor_id, r.created_at, NULL::uuid AS superseded_by_id,
       r.problem, r.problem_comment_id, r.scope, r.project_id
FROM current_records r
ORDER BY r.created_at ASC, r.id ASC;

-- name: ListCurrentProjectDecisionRecords :many
WITH current_records AS (
    SELECT r.id, r.workspace_id, r.issue_id, r.comment_id, r.decided, r.rationale,
           r.supersedes_id, r.source_kind, r.actor_id, r.created_at, r.problem,
           r.problem_comment_id, r.scope, r.project_id
    FROM issue_decision_record r
    WHERE r.workspace_id = $1 AND r.project_id = $2 AND r.scope = 'project'
      AND NOT EXISTS (
          SELECT 1 FROM issue_decision_record replacement
          WHERE replacement.supersedes_id = r.id
      )
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT 5
)
SELECT r.id, r.workspace_id, r.issue_id, r.comment_id, r.decided, r.rationale,
       r.supersedes_id, r.source_kind, r.actor_id, r.created_at, NULL::uuid AS superseded_by_id,
       r.problem, r.problem_comment_id, r.scope, r.project_id
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
