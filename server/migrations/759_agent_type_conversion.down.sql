-- Restore the M-501 Role-only invariant. Refuse to roll back while a plain
-- Agent still carries an inactive tier_role: an operator must clear those
-- values first rather than have the down migration silently reject them.
DO $$
DECLARE
    live_rows BIGINT;
BEGIN
    SELECT count(*) INTO live_rows
    FROM agent
    WHERE subtype <> 'role' AND tier_role IS NOT NULL;
    IF live_rows > 0 THEN
        RAISE EXCEPTION
            'M-567 down aborted: % agent row(s) carry inactive tier_role; clear them first',
            live_rows;
    END IF;
END $$;

ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_tier_role_check;
ALTER TABLE agent ADD CONSTRAINT agent_tier_role_check
    CHECK (tier_role IS NULL OR (subtype = 'role'
           AND tier_role ~ '^[a-z0-9][a-z0-9-]{0,39}$'));
