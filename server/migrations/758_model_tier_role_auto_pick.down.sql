-- A tier-wide boolean cannot represent per-role differences. Refuse to drop
-- those differences rather than silently lose the configuration on rollback.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM model_tier_role mtr
        JOIN model_tier mt ON mt.id = mtr.tier_id
        WHERE mtr.auto_pick IS DISTINCT FROM mt.auto_pick
    ) THEN
        RAISE EXCEPTION 'cannot roll back per-role tier auto-pick while role settings differ from the legacy tier value';
    END IF;
END $$;

ALTER TABLE model_tier_role DROP COLUMN auto_pick;
