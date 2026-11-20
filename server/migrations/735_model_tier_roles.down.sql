-- M-423 rollback: drop the role/difficulty columns. Data in the dropped
-- columns is not recoverable; tier rows themselves are kept.
DROP INDEX IF EXISTS idx_model_tier_roles;
ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS tier_quality_unverified,
    DROP COLUMN IF EXISTS tier_quality_fallback;
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_role_check;
ALTER TABLE agent DROP COLUMN IF EXISTS role;
ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_quota_kind_check;
ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_roles_check;
ALTER TABLE model_tier
    DROP COLUMN IF EXISTS is_free,
    DROP COLUMN IF EXISTS quota_kind,
    DROP COLUMN IF EXISTS manual_only,
    DROP COLUMN IF EXISTS roles;
