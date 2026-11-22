-- Workspace role registry (M-497, Owner Q4). Free workspace-defined keys;
-- tier writes auto-register unknown keys, registry management API follows in
-- the UI card (M-498). No FKs per repo rule.

-- name: ListTierRolesByWorkspace :many
SELECT * FROM tier_role
WHERE workspace_id = $1
ORDER BY position ASC, key ASC;

-- name: MaxTierRolePosition :one
SELECT COALESCE(MAX(position), -1)::int AS max_position FROM tier_role
WHERE workspace_id = $1;

-- name: InsertTierRoleIgnore :exec
INSERT INTO tier_role (workspace_id, key, position)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;
