-- M-423: per-role tier tables + difficulty-aware routing.
--
-- model_tier gains:
--   roles        TEXT[] — subset of the six role names; '{}' = legacy/unconfigured.
--   manual_only  never auto-resolved; explicit pick requires workspace owner.
--   quota_kind   display/scheduling hint (no resolver logic).
--   is_free      display hint (no resolver logic).
-- agent gains role TEXT NULL — NULL = legacy, no role filtering until set.
-- agent_task_queue gains two run-record flags:
--   tier_quality_fallback — stamped tier did not meet the required floor.
--   tier_quality_unverified — stamped tier had no measured quality (NULL).
-- No FKs per repo rule. 728 owns the table; this migration only adds columns.
ALTER TABLE model_tier
    ADD COLUMN IF NOT EXISTS roles TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS manual_only BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS quota_kind TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS is_free BOOLEAN NOT NULL DEFAULT TRUE;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_tier_roles_check'
    ) THEN
        ALTER TABLE model_tier
            ADD CONSTRAINT model_tier_roles_check
            CHECK (roles <@ ARRAY['architect','leader','reviewer','orchestrator','engineer','planning']);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'model_tier_quota_kind_check'
    ) THEN
        ALTER TABLE model_tier
            ADD CONSTRAINT model_tier_quota_kind_check
            CHECK (quota_kind IN ('daily','monthly','five_hour_weekly','none'));
    END IF;
END $$;

ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS role TEXT;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'agent_role_check'
    ) THEN
        ALTER TABLE agent
            ADD CONSTRAINT agent_role_check
            CHECK (role IS NULL OR role IN ('architect','leader','reviewer','orchestrator','engineer','planning'));
    END IF;
END $$;

ALTER TABLE agent_task_queue
    ADD COLUMN IF NOT EXISTS tier_quality_fallback BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS tier_quality_unverified BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_model_tier_roles
    ON model_tier USING GIN (roles);
