-- M-350: workspace-level model tiers (Owner-approved Candidate B).
-- A tier names a runtime/provider/model combo with an order index (0 first,
-- 99 last), display cost/quality, and provider_slots (max simultaneous
-- sessions on that runtime/provider; e.g. Freebuff = 1). No foreign keys,
-- per repo rule: teardown stays application-owned.
CREATE TABLE model_tier (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    idx INTEGER NOT NULL DEFAULT 0,
    runtime_id UUID,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    thinking_level TEXT,
    service_tier TEXT,
    cost NUMERIC,
    quality INTEGER,
    provider_slots INTEGER NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT model_tier_idx_range CHECK (idx >= 0 AND idx <= 99),
    CONSTRAINT model_tier_slots_positive CHECK (provider_slots >= 1)
);
