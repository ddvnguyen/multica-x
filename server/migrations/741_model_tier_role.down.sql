-- M-497 down: drop the per-role order table (global model_tier.idx/roles
-- remain the fallback, as before this migration).
DROP TABLE IF EXISTS model_tier_role;
