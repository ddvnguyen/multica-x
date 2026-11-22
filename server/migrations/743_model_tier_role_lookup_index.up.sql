CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_model_tier_role_lookup
    ON model_tier_role (workspace_id, role, idx);
