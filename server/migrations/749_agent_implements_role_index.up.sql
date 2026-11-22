-- M-501: resolve the implementing Agents of a Role for inheritance and
-- visibility checks. Partial index keeps ordinary Agents out of the tree.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_implements_role
    ON agent (workspace_id, implements_role_id) WHERE implements_role_id IS NOT NULL;
