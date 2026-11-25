-- Roll back only the lookup index; the shadow table itself rolls back in 761.
DROP INDEX CONCURRENTLY IF EXISTS jev_shadow_decision_point_created_idx;
