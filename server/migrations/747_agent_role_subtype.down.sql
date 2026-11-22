-- M-501 slice 1 rollback. Refuse to delete live Roles or live Agent->Role
-- links: an operator must export/retarget them before the columns can go.
DO $$
DECLARE
    live_rows BIGINT;
BEGIN
    SELECT count(*) INTO live_rows
    FROM agent
    WHERE subtype = 'role'
       OR implements_role_id IS NOT NULL
       OR tier_role IS NOT NULL;
    IF live_rows > 0 THEN
        RAISE EXCEPTION
            'M-501 down aborted: % agent row(s) still use the Role subtype or carry Role-only fields; export or retarget them first',
            live_rows;
    END IF;
END $$;

ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_tier_role_check;
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_implements_role_check;
ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_subtype_check;
ALTER TABLE agent
    DROP COLUMN IF EXISTS tier_role,
    DROP COLUMN IF EXISTS implements_role_id,
    DROP COLUMN IF EXISTS subtype;
