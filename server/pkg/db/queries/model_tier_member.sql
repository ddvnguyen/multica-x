-- Model tier members (M-481). A tier profile groups ordered members; each
-- member is one (provider, model, runtime, thinking, slots, cooldown) mapping.
-- No FKs per repo rule.

-- name: CreateModelTierMember :one
INSERT INTO model_tier_member (
    tier_id, position, provider, model, thinking_level,
    runtime_id, provider_slots, cooldown_until, enabled
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: GetModelTierMember :one
SELECT * FROM model_tier_member WHERE id = $1;

-- name: GetModelTierMemberInTier :one
SELECT * FROM model_tier_member WHERE id = $1 AND tier_id = $2;

-- name: ListModelTierMembers :many
SELECT * FROM model_tier_member
WHERE tier_id = $1
ORDER BY position ASC, created_at ASC, id ASC;

-- name: ListEnabledModelTierMembersByTier :many
SELECT * FROM model_tier_member
WHERE tier_id = $1 AND enabled = TRUE
ORDER BY position ASC, created_at ASC, id ASC;

-- name: UpdateModelTierMember :one
UPDATE model_tier_member
SET position = $2,
    provider = $3,
    model = $4,
    thinking_level = $5,
    runtime_id = $6,
    provider_slots = $7,
    cooldown_until = $8,
    enabled = $9,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteModelTierMember :exec
DELETE FROM model_tier_member WHERE id = $1;

-- name: DeleteModelTierMembersByTier :exec
DELETE FROM model_tier_member WHERE tier_id = $1;

-- name: ReorderModelTierMembers :exec
UPDATE model_tier_member m
SET position = o.pos,
    updated_at = now()
FROM (SELECT u.id, (u.ord - 1)::int AS pos
      FROM unnest(@ids::uuid[]) WITH ORDINALITY AS u(id, ord)) o
WHERE m.tier_id = @tier_id AND m.id = o.id;
