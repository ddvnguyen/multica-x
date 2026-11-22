CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_model_tier_role_tier_role
    ON model_tier_role (tier_id, role);
