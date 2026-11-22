-- M-497 (Owner Q4): the workspace's role registry. Roles are free
-- workspace-defined keys — the Owner adds and removes roles in Tier config
-- and each tier carries a multi-select of them — so neither this table nor
-- model_tier_role constrains keys to a fixed set; only the slug shape is
-- checked (keys ride mentions, URLs and tab params).
--
-- Seeded with the three classic roles for every workspace. Tiers keep
-- working when a key has no tiers yet (an empty tab), and tier writes
-- auto-register unknown keys at the end of the registry. Registry
-- create/rename/delete API follows in the UI card (M-498).
--
-- No foreign keys (repo rule). The unique index lives in 745 (CONCURRENTLY).

CREATE TABLE IF NOT EXISTS tier_role (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    key TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tier_role_key_check
        CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    CONSTRAINT tier_role_position_check
        CHECK (position >= 0)
);

-- Seed the three classic roles for every workspace (idempotent).
INSERT INTO tier_role (workspace_id, key, position)
SELECT w.id, k.key, k.pos
FROM workspace w
CROSS JOIN (VALUES ('architect', 0), ('management', 1), ('engineer', 2)) AS k(key, pos)
WHERE NOT EXISTS (
    SELECT 1 FROM tier_role t
    WHERE t.workspace_id = w.id AND t.key = k.key
);
