-- M-550: auto-pick is configured independently for each (tier, role).
ALTER TABLE model_tier_role
    ADD COLUMN auto_pick BOOLEAN NOT NULL DEFAULT TRUE;

-- Preserve the existing resolver behavior for every role row.
UPDATE model_tier_role mtr
SET auto_pick = mt.auto_pick
FROM model_tier mt
WHERE mt.id = mtr.tier_id;
