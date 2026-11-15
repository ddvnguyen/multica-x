ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS tier_id,
    DROP COLUMN IF EXISTS service_tier_override,
    DROP COLUMN IF EXISTS thinking_level_override,
    DROP COLUMN IF EXISTS model_override;

DROP TABLE IF EXISTS agent_tier;

ALTER TABLE agent
    DROP COLUMN IF EXISTS use_tiers;
