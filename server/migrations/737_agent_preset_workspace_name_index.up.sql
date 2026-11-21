CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_preset_workspace_name
    ON agent_preset (workspace_id, lower(name));
