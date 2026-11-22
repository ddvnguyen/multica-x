-- M-501: list Roles in a workspace without scanning ordinary Agents.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_role_subtype
    ON agent (workspace_id, created_at) WHERE subtype = 'role';
