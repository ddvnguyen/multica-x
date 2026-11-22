-- M-497 (Owner Q4): roles are free workspace-defined keys, so the fixed
-- 7-name CHECK vocabularies on model_tier.roles and agent.role go away.
-- Validation moves to application code (slug shape); the tier_role registry
-- (744) is the workspace's role set.
--
-- Down re-adds the old checks; it fails if custom roles are in use, which is
-- the honest signal that the rollback needs the custom rows removed first.

ALTER TABLE model_tier DROP CONSTRAINT IF EXISTS model_tier_roles_check;
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_role_check;
