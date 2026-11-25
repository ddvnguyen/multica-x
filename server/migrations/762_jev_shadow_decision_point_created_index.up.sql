-- Jev shadow decisions lookup (M-580 / M-584): compare one decision_point's
-- recent rows. Single-statement CONCURRENTLY build per AGENTS.md; the runner
-- executes it outside an explicit transaction. IF NOT EXISTS + the
-- concurrentIndexCleanups hook make a lane re-apply (after the 760 -> 761
-- renumber) and an interrupted-build retry safe.
CREATE INDEX CONCURRENTLY IF NOT EXISTS jev_shadow_decision_point_created_idx
    ON jev_shadow_decision (decision_point, created_at DESC);
