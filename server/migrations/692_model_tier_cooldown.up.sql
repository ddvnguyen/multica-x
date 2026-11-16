-- M-352: provider cooldown on a model tier.
--
-- A tier-stamped task that failed its provider with a capacity/rate-limit or
-- quota/billing error arms cooldown_until on that tier. The enqueue resolver
-- skips a cooling tier in idx order, so the next task falls to the next tier
-- instead of spending another run on a provider that is refusing right now.
-- NULL means "not cooling"; the deadline is a hard floor, not a lockout — the
-- tier re-enters idx order by itself at the first resolve after it expires.
--
-- No retry-after reaches the server today (the daemon reports the failure text
-- only), so the fail path arms the default window. A daemon that starts
-- carrying the hint can shorten or lengthen this column without a migration.
ALTER TABLE model_tier
    ADD COLUMN cooldown_until TIMESTAMPTZ;
