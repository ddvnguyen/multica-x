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

-- Registry management (M-498). The registry is the workspace's role set; a
-- tier write auto-registers unknown keys, and these three let an admin curate
-- the set explicitly. Rename and delete move the per-role rows with the key so
-- a tier never keeps a position in a role that no longer exists.

-- name: CreateTierRole :one
INSERT INTO tier_role (workspace_id, key, position)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetTierRole :one
SELECT * FROM tier_role
WHERE workspace_id = $1 AND key = $2;

-- name: RenameTierRole :execrows
UPDATE tier_role SET key = @new_key
WHERE workspace_id = @workspace_id AND key = @old_key;

-- name: DeleteTierRole :execrows
DELETE FROM tier_role
WHERE workspace_id = $1 AND key = $2;

-- name: RenameModelTierRoleKey :exec
UPDATE model_tier_role SET role = @new_key
WHERE workspace_id = @workspace_id AND role = @old_key;

-- The legacy array the old binary still reads. array_replace keeps the
-- element order and touches nothing else.
-- name: RenameModelTierRoleInTiers :exec
UPDATE model_tier SET roles = array_replace(roles, @old_key::text, @new_key::text)
WHERE workspace_id = @workspace_id AND @old_key::text = ANY(roles);

-- name: CountTiersUsingRole :one
SELECT COUNT(*)::int AS tier_count FROM model_tier_role
WHERE workspace_id = $1 AND role = $2;
