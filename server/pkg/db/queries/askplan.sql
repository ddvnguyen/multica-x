-- S1-A1 (M-118) ask/plan queries: question cards (585) and revisioned plans
-- (586/587). All state transitions are compare-and-set on the row's current
-- status (and, for plans, on the revision fence), so a stale tool call or a
-- double verdict is a business 409, never a lost update. No foreign keys
-- (538 house style); teardown is application-owned via the task-cancel settle
-- below.

-- name: CreateAgentQuestionCard :one
INSERT INTO agent_question_card (
    id, workspace_id, issue_id, task_id, agent_id,
    status, context, questions
) VALUES (
    @id, @workspace_id, @issue_id, @task_id, @agent_id,
    'open', sqlc.narg('context'), @questions
)
RETURNING *;

-- name: GetAgentQuestionCard :one
SELECT * FROM agent_question_card WHERE id = $1;

-- name: ListOpenQuestionCardsByIssue :many
SELECT * FROM agent_question_card
WHERE issue_id = $1 AND status = 'open'
ORDER BY created_at DESC;

-- name: AnswerAgentQuestionCard :one
-- Single-answer enforcement (team-room assertDecisionOpen): the CAS on
-- status='open' means a second answer matches no row → pgx.ErrNoRows → the
-- handler maps it to 409. answers is stored verbatim in question order.
UPDATE agent_question_card
SET status = 'answered',
    answers = @answers,
    answered_by = @answered_by,
    answered_at = now(),
    updated_at = now()
WHERE id = $1 AND status = 'open'
RETURNING *;

-- name: WithdrawQuestionCardsByTask :exec
-- Cancel-while-parked settle (Architect amendment 3): withdrawing every open
-- card of a task batch just moved to a terminal status. No-op for tasks that
-- never parked (they own no open card).
UPDATE agent_question_card
SET status = 'withdrawn',
    withdrawn_at = now(),
    updated_at = now()
WHERE task_id = ANY(@task_ids::uuid[]) AND status = 'open';

-- name: CreateAgentPlan :one
INSERT INTO agent_plan (
    id, workspace_id, issue_id, task_id, agent_id, title, status, sections
) VALUES (
    @id, @workspace_id, @issue_id, @task_id, @agent_id, @title, 'draft', @sections
)
RETURNING *;

-- name: GetAgentPlan :one
SELECT * FROM agent_plan WHERE id = $1;

-- name: GetOpenAgentPlanByIssue :one
SELECT * FROM agent_plan
WHERE issue_id = $1 AND status IN ('draft', 'in_review', 'changes_requested')
LIMIT 1;

-- name: WriteAgentPlanSection :one
-- Section write with the revision fence: base_revision must equal the row's
-- current revision or the CAS matches no row (handler → 409 + current
-- revision, the port of team-room's stale_doc_revision business outcome).
-- The whole sections array is rewritten (single-writer tool contract); the
-- revision bumps on every accepted write, which is what fences the next
-- write, the submit and the verdict.
UPDATE agent_plan
SET sections = @sections,
    revision = revision + 1,
    updated_at = now()
WHERE id = $1 AND revision = @base_revision
  AND status IN ('draft', 'changes_requested')
RETURNING *;

-- name: SubmitAgentPlan :one
-- submit: draft or changes_requested -> in_review, fenced by revision.
-- Refusing a double submit at the same revision falls out of the CAS (a plan
-- already in_review matches neither status). Re-checking the planning gate is
-- the handler's job (it needs issue status + active tasks).
-- task_id is refreshed to the submitting task: after a changes_requested →
-- resume cycle the caller is a NEW task row, and both the block transition
-- and the cancel-while-parked settle must reach the plan through the task
-- that is parking NOW, not the original proposer.
UPDATE agent_plan
SET status = 'in_review',
    submitted_revision = @revision,
    task_id = @task_id,
    updated_at = now()
WHERE id = $1 AND revision = @revision
  AND status IN ('draft', 'changes_requested')
RETURNING *;

-- name: RecordAgentPlanReview :one
-- Verdict history append (587). Nothing here is ever updated.
INSERT INTO agent_plan_review (
    id, plan_id, plan_revision, verdict, comments, note, reviewed_by
) VALUES (
    @id, @plan_id, @plan_revision, @verdict, @comments, sqlc.narg('note'), @reviewed_by
)
RETURNING *;

-- name: ApproveAgentPlan :one
-- approve: in_review -> approved, fenced on the reviewed revision. Provenance
-- (approved_by/at) lives here because additive-only forbids an issue column.
UPDATE agent_plan
SET status = 'approved',
    approved_by = @reviewed_by,
    approved_at = now(),
    updated_at = now()
WHERE id = $1 AND status = 'in_review' AND submitted_revision = @revision
RETURNING *;

-- name: RequestAgentPlanChanges :one
-- changes_requested: in_review -> changes_requested. The revision does NOT
-- bump (team-room rule): the agent revises the same plan and resubmits at the
-- new revision its own write produced.
UPDATE agent_plan
SET status = 'changes_requested',
    updated_at = now()
WHERE id = $1 AND status = 'in_review' AND submitted_revision = @revision
RETURNING *;

-- name: CancelAgentPlan :one
-- cancel: any open state -> cancelled; final; frees the issue for a new plan
-- (idx_ap_one_open only covers the open statuses).
UPDATE agent_plan
SET status = 'cancelled',
    cancelled_at = now(),
    updated_at = now()
WHERE id = $1 AND status IN ('draft', 'in_review', 'changes_requested')
RETURNING *;

-- name: CancelPlansByTask :exec
-- Cancel-while-parked settle (amendment 3): same shape as
-- WithdrawQuestionCardsByTask. No-op for tasks with no open plan.
UPDATE agent_plan
SET status = 'cancelled',
    cancelled_at = now(),
    updated_at = now()
WHERE task_id = ANY(@task_ids::uuid[]) AND status IN ('draft', 'in_review', 'changes_requested');

-- name: CountActiveTasksForIssueExcluding :one
-- The plan-creation gate: "no other active task" (Architect-endorsed reading —
-- the caller is excluded because it is by definition running while it calls
-- plan create). 'blocked' is NOT active (parked holds no slot) and 'deferred'
-- is scheduled work, not executing work; both stay out of the tuple.
SELECT count(*) AS active
FROM agent_task_queue
WHERE issue_id = $1
  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
  AND id <> sqlc.narg('excluded_task_id')::uuid;
