-- M-481 rollback: drop member snapshots, member table, and profile columns.
-- Post-upgrade member edits cannot be represented in the pre-481 schema; this
-- down migration restores the legacy tier columns that were kept in sync but
-- cannot reconstruct multiple members. Operators should only roll back before
-- member rows diverge from the backfilled single-member shape.
ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS tier_member_id;

DROP TABLE IF EXISTS model_tier_member;

-- Restore manual_only from auto_pick before dropping auto_pick.
UPDATE model_tier SET manual_only = NOT auto_pick WHERE auto_pick IS NOT NULL;

ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_quota_kinds_check;
ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_compact_at_pct_check;
ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_ctx_max_tokens_check;

ALTER TABLE model_tier
    DROP COLUMN IF EXISTS compact_at_pct,
    DROP COLUMN IF EXISTS ctx_max_tokens,
    DROP COLUMN IF EXISTS quota_kinds,
    DROP COLUMN IF EXISTS auto_pick;
