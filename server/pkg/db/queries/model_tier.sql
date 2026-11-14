-- Workspace model tiers (M-350). No FKs per repo rule: related rows are
-- validated and cleaned up in application code.

-- name: CreateModelTier :one
INSERT INTO model_tier (
    workspace_id, name, idx, runtime_id, provider, model,
    thinking_level, service_tier, cost, quality, provider_slots, enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
) RETURNING *;

-- name: GetModelTierInWorkspace :one
SELECT * FROM model_tier
WHERE id = $1 AND workspace_id = $2;

-- name: ListModelTiers :many
SELECT * FROM model_tier
WHERE workspace_id = $1
ORDER BY idx ASC, created_at ASC;

-- name: UpdateModelTier :one
UPDATE model_tier
SET name = $2,
    idx = $3,
    runtime_id = $4,
    provider = $5,
    model = $6,
    thinking_level = $7,
    service_tier = $8,
    cost = $9,
    quality = $10,
    provider_slots = $11,
    enabled = $12,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteModelTier :exec
DELETE FROM model_tier WHERE id = $1;

-- name: ReorderModelTiers :exec
-- The caller passes the full ordered id list; position in the array becomes
-- the new idx (0 first … 99 last). Rows not in the list keep their idx.
UPDATE model_tier t
SET idx = o.pos,
    updated_at = now()
FROM (SELECT u.id, (u.ord - 1) AS pos
      FROM unnest(@ids::uuid[]) WITH ORDINALITY AS u(id, ord)) o
WHERE t.workspace_id = @workspace_id AND t.id = o.id;
