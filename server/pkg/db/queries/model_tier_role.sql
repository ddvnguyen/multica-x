-- Per-role tier order (M-497). One row per (tier, canonical role) names
-- that role's position; the resolver and the ?role= list order by it.
-- No FKs per repo rule; tier delete cleans up in application code.

-- name: ListModelTierRolesByWorkspace :many
SELECT * FROM model_tier_role
WHERE workspace_id = $1
ORDER BY role ASC, idx ASC, created_at ASC, tier_id ASC;

-- name: ListModelTierRolesByTier :many
SELECT * FROM model_tier_role
WHERE tier_id = $1
ORDER BY role ASC;

-- name: ListModelTierRoleIdxForTiers :many
SELECT tier_id, idx FROM model_tier_role
WHERE tier_id = ANY(@ids::uuid[]) AND role = @role::text;

-- name: UpsertModelTierRole :one
INSERT INTO model_tier_role (workspace_id, tier_id, role, idx)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tier_id, role) DO UPDATE
SET idx = EXCLUDED.idx
RETURNING *;

-- name: MaxModelTierRoleIdx :one
SELECT COALESCE(MAX(idx), -1)::int AS max_idx FROM model_tier_role
WHERE workspace_id = $1 AND role = $2;

-- name: DeleteModelTierRolesNotIn :exec
DELETE FROM model_tier_role
WHERE tier_id = $1 AND NOT (role = ANY(@roles::text[]));

-- name: DeleteModelTierRolesByTier :exec
DELETE FROM model_tier_role WHERE tier_id = $1;
