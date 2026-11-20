-- Index for the ordered workspace listing backing Settings → Tiers.
-- Own single-statement file: CREATE INDEX CONCURRENTLY cannot run inside an
-- explicit transaction (repo rule).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_model_tier_workspace
    ON model_tier (workspace_id, idx ASC, created_at ASC);
