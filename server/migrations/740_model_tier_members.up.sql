-- M-481: tier profiles + ordered members. A model_tier becomes a profile that
-- groups multiple (harness/runtime provider, model) members; the resolver picks
-- the tier, then the first available member in position order. Cooldown and
-- provider slots move to the member; a 429 on one member must not block another
-- member of the same tier. No foreign keys (repo rule); indexes that must be
-- built CONCURRENTLY live in their own migration files.
--
-- Backward compat: old tier columns (runtime_id, provider, model, thinking_level,
-- service_tier, provider_slots, cooldown_until, quota_kind, manual_only) are kept
-- and backfilled so pre-481 binaries keep working. New code writes both the
-- member rows and the legacy tier columns; legacy tasks with no member_id keep
-- the old claim/cooldown behavior.

ALTER TABLE model_tier
    ADD COLUMN IF NOT EXISTS auto_pick BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS quota_kinds TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS ctx_max_tokens INTEGER NOT NULL DEFAULT 280000,
    ADD COLUMN IF NOT EXISTS compact_at_pct INTEGER NOT NULL DEFAULT 75;

-- auto_pick replaces manual_only (auto_pick = NOT manual_only).
UPDATE model_tier SET auto_pick = NOT manual_only;

-- Map legacy scalar quota_kind to the new multi-select quota_kinds, preserving
-- is_free independently (never invented from quota_kinds).
UPDATE model_tier
SET quota_kinds = CASE quota_kind
    WHEN 'daily' THEN ARRAY['quota_daily']::text[]
    WHEN 'monthly' THEN ARRAY['quota_monthly']::text[]
    WHEN 'five_hour_weekly' THEN ARRAY['quota_5_hours', 'quota_weekly']::text[]
    ELSE '{}'::text[]
END;

-- Expand role vocabulary to include 'management' while keeping legacy roles
-- during transition (M-481).
ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_roles_check;
ALTER TABLE model_tier
    ADD CONSTRAINT model_tier_roles_check
    CHECK (roles <@ ARRAY['architect','management','engineer','leader','reviewer','orchestrator','planning']::text[]);

ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_role_check;
ALTER TABLE agent
    ADD CONSTRAINT agent_role_check
    CHECK (role IS NULL OR role IN ('architect','management','engineer','leader','reviewer','orchestrator','planning'));

-- Migrate existing role sets: leader/reviewer/orchestrator -> management,
-- planning -> architect. Deduplicate.
UPDATE model_tier
SET roles = (
    SELECT ARRAY(
        SELECT DISTINCT mapped FROM (
            SELECT CASE
                WHEN r IN ('leader','reviewer','orchestrator') THEN 'management'
                WHEN r = 'planning' THEN 'architect'
                ELSE r
            END AS mapped
            FROM unnest(roles) AS r
        ) s
        ORDER BY mapped
    )
)
WHERE roles && ARRAY['leader','reviewer','orchestrator','planning']::text[];

UPDATE agent
SET role = CASE
    WHEN role IN ('leader','reviewer','orchestrator') THEN 'management'
    WHEN role = 'planning' THEN 'architect'
    ELSE role
END
WHERE role IN ('leader','reviewer','orchestrator','planning');

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_tier_ctx_max_tokens_check'
    ) THEN
        ALTER TABLE model_tier
            ADD CONSTRAINT model_tier_ctx_max_tokens_check
            CHECK (ctx_max_tokens >= 1024 AND ctx_max_tokens <= 2000000);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_tier_compact_at_pct_check'
    ) THEN
        ALTER TABLE model_tier
            ADD CONSTRAINT model_tier_compact_at_pct_check
            CHECK (compact_at_pct >= 1 AND compact_at_pct <= 100);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_tier_quota_kinds_check'
    ) THEN
        ALTER TABLE model_tier
            ADD CONSTRAINT model_tier_quota_kinds_check
            CHECK (quota_kinds <@ ARRAY[
                'free_daily', 'free', 'local',
                'quota_5_hours', 'quota_daily', 'quota_weekly', 'quota_monthly'
            ]::text[]);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS model_tier_member (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_id UUID NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    thinking_level TEXT,
    runtime_id UUID,
    provider_slots INTEGER NOT NULL DEFAULT 1,
    cooldown_until TIMESTAMPTZ,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT model_tier_member_position_check CHECK (position >= 0),
    CONSTRAINT model_tier_member_slots_positive CHECK (provider_slots >= 1)
);

-- One member per existing tier, preserving the tier's runtime/model/thinking/
-- slots/cooldown so legacy behavior and stamped tasks remain valid.
INSERT INTO model_tier_member (
    tier_id, position, provider, model, thinking_level,
    runtime_id, provider_slots, cooldown_until, enabled, created_at, updated_at
)
SELECT
    t.id, 0, t.provider, t.model, t.thinking_level,
    t.runtime_id, t.provider_slots, t.cooldown_until, t.enabled, t.created_at, t.updated_at
FROM model_tier t
WHERE NOT EXISTS (
    SELECT 1 FROM model_tier_member m WHERE m.tier_id = t.id
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_tier_member_tier_position
    ON model_tier_member (tier_id, position);
CREATE INDEX IF NOT EXISTS idx_model_tier_member_tier
    ON model_tier_member (tier_id, position ASC, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_model_tier_member_runtime
    ON model_tier_member (runtime_id);

-- Task snapshot: which member was stamped. NULL = legacy/pre-member task.
ALTER TABLE agent_task_queue
    ADD COLUMN IF NOT EXISTS tier_member_id UUID;
