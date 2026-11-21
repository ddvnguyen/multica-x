-- M-467: a named quick pick pairing a position agent with one workspace tier.
-- Related rows are validated and removed in application code (no FKs).
CREATE TABLE agent_preset (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name TEXT NOT NULL,
    agent_id UUID NOT NULL,
    tier_id UUID NOT NULL,
    legacy_agent_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
