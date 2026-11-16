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

-- Agent tier selection (M-351). The join behind the Agent page "Use tiers"
-- switch: an agent with use_tiers stores its chosen tier ids here. No FKs per
-- repo rule. ListAgentTiers joins model_tier so the resolver walks enabled
-- tiers ordered by idx in one round trip; a tier deleted out from under an
-- agent simply stops appearing (UI shows "unavailable", never silently
-- reassigns). SetAgentTiers replaces the whole selection: delete-then-insert
-- in one statement keeps partial writes impossible without a tx.

-- name: ListAgentTiers :many
SELECT t.* FROM model_tier t
JOIN agent_tier at ON at.tier_id = t.id
WHERE at.agent_id = $1
ORDER BY t.idx ASC, t.created_at ASC;

-- name: ListAgentTierIDs :many
SELECT tier_id FROM agent_tier
WHERE agent_id = $1;

-- name: SetAgentTiers :execrows
DELETE FROM agent_tier WHERE agent_id = sqlc.arg('agent_id')::uuid;

-- name: AddAgentTier :execrows
INSERT INTO agent_tier (agent_id, tier_id)
VALUES (sqlc.arg('agent_id')::uuid, sqlc.arg('tier_id')::uuid)
ON CONFLICT DO NOTHING;

-- name: ClearAgentTiers :exec
DELETE FROM agent_tier WHERE agent_id = $1;

-- name: DeleteTierAgentSelections :exec
DELETE FROM agent_tier WHERE tier_id = $1;

-- M-352: arms a tier's provider cooldown (NULL clears it). Runtime state, not
-- an edit of the tier, so updated_at is deliberately left alone: the Settings
-- row keeps showing when an admin last changed the tier itself.
-- name: SetModelTierCooldown :exec
UPDATE model_tier SET cooldown_until = $2 WHERE id = $1;

-- M-351: claim-time provider_slots enforcement. A tier-stamped task claimed
-- inside the runtime capacity lock asks whether its tier is over capacity:
-- more active rows on the tier's runtime than provider_slots. TRUE means the
-- claim must requeue, not dispatch — two enqueues resolved the same tier
-- before either inserted. Enqueue already avoids full tiers; this closes the
-- race at the serialization point.
-- name: TierTaskOverCapacity :one
SELECT EXISTS (
    SELECT 1 FROM model_tier t
    WHERE t.id = (SELECT atq.tier_id FROM agent_task_queue atq WHERE atq.id = $1)
      AND t.enabled
      AND (
            SELECT count(*) FROM agent_task_queue active
            WHERE active.runtime_id = t.runtime_id
              AND active.status IN ('dispatched', 'running', 'waiting_local_directory')
              AND active.id <> $1
          ) >= t.provider_slots
) AS over_capacity;
