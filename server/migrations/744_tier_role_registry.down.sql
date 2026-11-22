-- M-497 down: drop the role registry. Per-role order rows (model_tier_role)
-- are dropped by the 741 down migration.
DELETE FROM tier_role;
DROP TABLE IF EXISTS tier_role;
