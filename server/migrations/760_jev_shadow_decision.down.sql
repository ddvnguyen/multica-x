-- The table holds observations only; dropping it loses the shadow log and
-- nothing the product reads.
DROP INDEX IF EXISTS jev_shadow_decision_point_created_idx;
DROP TABLE IF EXISTS jev_shadow_decision;
