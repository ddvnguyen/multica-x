-- name: CreateWorkspaceAccount :one
INSERT INTO workspace_account (
    workspace_id, provider, label, endpoint, credential_ref, enabled, created_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: GetWorkspaceAccount :one
SELECT * FROM workspace_account
WHERE id = $1;

-- name: GetWorkspaceAccountInWorkspace :one
SELECT * FROM workspace_account
WHERE id = $1 AND workspace_id = $2;

-- name: ListWorkspaceAccounts :many
SELECT * FROM workspace_account
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: ListWorkspaceAccountsByProvider :many
SELECT * FROM workspace_account
WHERE workspace_id = $1 AND provider = $2 AND enabled = TRUE
ORDER BY created_at ASC;

-- name: UpdateWorkspaceAccount :one
UPDATE workspace_account
SET label = $2,
    endpoint = $3,
    credential_ref = $4,
    enabled = $5,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteWorkspaceAccount :exec
DELETE FROM workspace_account WHERE id = $1;

-- name: DemoteAgentsOnAccountDelete :exec
UPDATE agent
SET account_mode = 'default', account_id = NULL, updated_at = now()
WHERE account_id = $1;
-- name: ListAccountsForDispatch :many
-- The enabled accounts of one provider, least busy first. "Busy" is the number
-- of dispatched/running tasks holding the account; ties break on the account
-- that has been idle longest, so `any` spreads runs instead of piling on one.
SELECT a.id, a.label, a.endpoint, a.credential_ref,
       COALESCE(l.active, 0)::int AS active_tasks
FROM workspace_account a
LEFT JOIN (
    SELECT account_id,
           COUNT(*) FILTER (WHERE status IN ('dispatched', 'running')) AS active,
           MAX(COALESCE(started_at, dispatched_at)) AS last_used
    FROM agent_task_queue
    WHERE account_id IS NOT NULL
    GROUP BY account_id
) l ON l.account_id = a.id
WHERE a.workspace_id = $1 AND a.provider = $2 AND a.enabled = TRUE
ORDER BY COALESCE(l.active, 0) ASC, l.last_used ASC NULLS FIRST, a.created_at ASC;

-- name: AgentAccountBlocked :one
-- Whether the agent must wait for an account to free up before a task is
-- claimed. `specific` is blocked while its account holds a dispatched/running
-- task; `any` only when every enabled account does. An agent with no usable
-- account (`default`, or `any` with none configured) is never blocked.
SELECT CASE
    WHEN ag.account_mode = 'specific' THEN EXISTS (
        SELECT 1 FROM agent_task_queue t
        WHERE t.account_id = ag.account_id AND t.status IN ('dispatched', 'running'))
    WHEN ag.account_mode = 'any' THEN
        EXISTS (SELECT 1 FROM workspace_account a
                WHERE a.workspace_id = ag.workspace_id AND a.provider = 'freebuff-desktop' AND a.enabled)
        AND NOT EXISTS (SELECT 1 FROM workspace_account a
                WHERE a.workspace_id = ag.workspace_id AND a.provider = 'freebuff-desktop' AND a.enabled
                  AND NOT EXISTS (SELECT 1 FROM agent_task_queue t
                                  WHERE t.account_id = a.id AND t.status IN ('dispatched', 'running')))
    ELSE FALSE
END::boolean AS blocked
FROM agent ag
WHERE ag.id = $1;
