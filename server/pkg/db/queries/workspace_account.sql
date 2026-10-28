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