-- M-567: a Role converting back to a plain Agent keeps its stored tier_role
-- inactive so it can be restored if the Agent is switched back. The M-501
-- CHECK made that impossible by requiring subtype = 'role' whenever
-- tier_role is set. Widen only the slug-shape guard: no columns, no data
-- changes and no foreign keys, so older binaries keep working unchanged.
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_tier_role_check;
ALTER TABLE agent ADD CONSTRAINT agent_tier_role_check
    CHECK (tier_role IS NULL OR tier_role ~ '^[a-z0-9][a-z0-9-]{0,39}$');
