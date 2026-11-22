-- M-497: per-role tier order. A tier's position is tracked per role so
-- reordering Engineer never moves Architect. model_tier.roles/idx stay for
-- the old binary during the transition; this table is the source of truth
-- and the API derives roles from it.
--
-- Roles are free workspace-defined keys (Owner Q4 on M-497): the role set
-- lives in the tier_role registry (migration 744), seeded with the three
-- classic roles. Only the slug shape is constrained here so keys survive
-- mentions, URLs and tab params.
--
-- No foreign keys (repo rule); application code cleans up role rows on tier
-- delete. Indexes that must be built CONCURRENTLY live in their own
-- migration files (742 unique, 743 lookup).

CREATE TABLE IF NOT EXISTS model_tier_role (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    tier_id UUID NOT NULL,
    role TEXT NOT NULL,
    idx INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT model_tier_role_key_check
        CHECK (role ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    CONSTRAINT model_tier_role_idx_check
        CHECK (idx >= 0 AND idx <= 99)
);

-- Backfill: one row per (tier, role) carrying the tier's current global idx.
-- Tiers with empty roles (legacy) get no row and keep matching legacy agents
-- with no role. Idempotent (rerunnable) via NOT EXISTS.
INSERT INTO model_tier_role (workspace_id, tier_id, role, idx)
SELECT t.workspace_id, t.id, r.role, t.idx
FROM model_tier t
CROSS JOIN LATERAL unnest(t.roles) AS r(role)
WHERE r.role ~ '^[a-z0-9][a-z0-9-]{0,39}$'
  AND NOT EXISTS (
      SELECT 1 FROM model_tier_role m
      WHERE m.tier_id = t.id AND m.role = r.role
  );
