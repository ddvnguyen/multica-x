-- M-351: agent "Use tiers" switch + task tier stamp.
--
-- Agent becomes a position: `use_tiers` flips the Agent page model picker to
-- a multi-select of workspace tiers. `agent_tier` is the join (ordered by the
-- tier's own idx at read time, not stored here). No FKs per repo rule:
-- teardown stays application-owned (tiers deleted out from under an agent
-- show as "unavailable", never cascade).
--
-- Task stamp: the enqueue-time resolver writes runtime_id + model/thinking/
-- service_tier overrides + tier_id on the row, so the claim path can route
-- without re-resolving. model_override rides the existing daemon
-- ModelOverride wire field for openclaw runtimes; thinking_level_override /
-- service_tier_override apply to every provider via resp.Agent fields.
ALTER TABLE agent
    ADD COLUMN use_tiers BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE agent_tier (
    agent_id UUID NOT NULL,
    tier_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_tier_pk PRIMARY KEY (agent_id, tier_id)
);

ALTER TABLE agent_task_queue
    ADD COLUMN model_override TEXT,
    ADD COLUMN thinking_level_override TEXT,
    ADD COLUMN service_tier_override TEXT,
    ADD COLUMN tier_id UUID,
    -- M-351 parked marker: a use_tiers enqueue with no free provider slot
    -- parks the row as queued with no runtime. The 251 CHECK (active rows
    -- need a runtime) is amended below to admit exactly this shape; the
    -- capacity-freed sweep clears the marker when it stamps the row.
    ADD COLUMN tier_parked BOOLEAN NOT NULL DEFAULT FALSE;

-- Admit the parked shape: queued + tier_parked rows wait on provider
-- capacity, not on a runtime. Everything else still fails closed.
ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_queue_active_requires_runtime;
ALTER TABLE agent_task_queue
    ADD CONSTRAINT agent_task_queue_active_requires_runtime
    CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL
           OR (status = 'queued' AND tier_parked))
    NOT VALID;
