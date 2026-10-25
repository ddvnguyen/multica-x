-- name: CreateQuestion :one
-- One row per question. A single AskUserQuestion tool call inserts 2-4 of
-- these sharing a group_id, which is what the daemon answers as a unit.
INSERT INTO question (
    workspace_id, issue_id, task_id, group_id, position, tool_call_id,
    asked_by_agent_id, addressee_type, addressee_id, header, prompt,
    options, multi_select, expires_at
) VALUES (
    sqlc.arg('workspace_id')::uuid,
    sqlc.narg('issue_id')::uuid,
    sqlc.narg('task_id')::uuid,
    sqlc.arg('group_id')::uuid,
    sqlc.arg('position')::int,
    sqlc.arg('tool_call_id')::text,
    sqlc.narg('asked_by_agent_id')::uuid,
    sqlc.narg('addressee_type')::text,
    sqlc.narg('addressee_id')::uuid,
    sqlc.arg('header')::text,
    sqlc.arg('prompt')::text,
    sqlc.arg('options')::jsonb,
    sqlc.arg('multi_select')::bool,
    sqlc.arg('expires_at')::timestamptz
)
RETURNING *;

-- name: GetQuestionGroup :many
SELECT * FROM question
WHERE group_id = sqlc.arg('group_id')::uuid
ORDER BY position ASC;

-- name: GetQuestion :one
SELECT * FROM question WHERE id = $1;

-- name: AnswerQuestion :one
-- Only an open question can be answered; the second answer of a race loses
-- and the handler reports it as already settled rather than overwriting.
UPDATE question SET
    status = 'answered',
    answer = sqlc.arg('answer')::text,
    answered_by_type = sqlc.arg('answered_by_type')::text,
    answered_by_id = sqlc.arg('answered_by_id')::uuid,
    answered_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')::uuid AND status = 'open'
RETURNING *;

-- name: ExpireQuestionGroup :exec
-- Cancels every still-open row of one tool call: the daemon gave up waiting
-- (task cancelled, daemon restarting) or the group hit its deadline.
UPDATE question SET status = 'expired', updated_at = now()
WHERE group_id = sqlc.arg('group_id')::uuid
  AND status = 'open'
  AND (
    sqlc.arg('force')::bool
    OR expires_at <= now()
  );

-- name: ListIssueQuestions :many
-- Newest first: the issue card shows what is open now, and a stale answered
-- question is history. `status` empty means every status.
SELECT * FROM question
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND (sqlc.arg('status')::text = '' OR status = sqlc.arg('status')::text)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit')::int;

-- name: ExpireQuestionsForTask :exec
-- A finished task can never consume an answer, so its open questions are
-- expired in the same breath. Called on task completion/failure/cancel.
UPDATE question SET status = 'expired', updated_at = now()
WHERE task_id = sqlc.arg('task_id')::uuid AND status = 'open';

-- name: ExpireStaleQuestionsForIssue :exec
-- Reads expire before they list. Without this an issue whose daemon died
-- would keep rendering a question past its deadline as answerable, and the
-- answer would fail with a 409 the reader could not explain.
UPDATE question SET status = 'expired', updated_at = now()
WHERE issue_id = sqlc.arg('issue_id')::uuid
  AND status = 'open'
  AND expires_at <= now();
