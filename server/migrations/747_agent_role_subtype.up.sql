-- M-501 slice 1: additive Role subtype on agent. A Role is an agent-backed
-- identity with no model/runtime configuration; `subtype` is a new discriminator
-- that is deliberately distinct from the existing system `kind` (user/system
-- execution carriers). `implements_role_id` links an Agent to the Role it
-- implements and `tier_role` is the Role's Owner-managed tier-role selection.
--
-- No foreign keys (repo rule): a deleted Role shows as "unavailable" to its
-- implementing Agents until the application cleans up the reference.
-- Old binaries never read or write these columns, so the defaults keep them
-- working unchanged. Index builds live in their own migrations. tier_role uses
-- the same slug shape as the M-497 tier_role registry; the registry itself is
-- the workspace's allowed role set and is enforced with the Role config slice.
ALTER TABLE agent
    ADD COLUMN IF NOT EXISTS subtype TEXT NOT NULL DEFAULT 'agent',
    ADD COLUMN IF NOT EXISTS implements_role_id UUID,
    ADD COLUMN IF NOT EXISTS tier_role TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agent'::regclass AND conname = 'agent_subtype_check'
    ) THEN
        ALTER TABLE agent
            ADD CONSTRAINT agent_subtype_check
            CHECK (subtype IN ('agent', 'role'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agent'::regclass AND conname = 'agent_implements_role_check'
    ) THEN
        ALTER TABLE agent
            ADD CONSTRAINT agent_implements_role_check
            CHECK (subtype = 'agent' OR implements_role_id IS NULL);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'agent'::regclass AND conname = 'agent_tier_role_check'
    ) THEN
        -- Role-only, slug-shaped, bounded: the same key format the M-497
        -- tier_role registry uses, so a Role can name a registry key today.
        ALTER TABLE agent
            ADD CONSTRAINT agent_tier_role_check
            CHECK (tier_role IS NULL OR (subtype = 'role'
                   AND tier_role ~ '^[a-z0-9][a-z0-9-]{0,39}$'));
    END IF;
END $$;
