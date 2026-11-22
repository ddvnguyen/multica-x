CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_tier_role_workspace_key
    ON tier_role (workspace_id, key);
