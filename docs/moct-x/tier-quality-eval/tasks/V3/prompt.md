# Task V3 — code review: reversible migration

You are reviewing a real merged pull-request diff from the `multica-x` repo (M-351). Do not write code files; answer in chat.

## Context

PostgreSQL migrations are numbered files applied in order; `down` must return the schema to the previous state. Migration 251 added the constraint `agent_task_queue_active_requires_runtime`, and production rows already exist.

## Diff excerpt

```diff
--- a/server/migrations/691_agent_use_tiers.up.sql
+++ b/server/migrations/691_agent_use_tiers.up.sql
+ALTER TABLE agent_task_queue
+    ADD COLUMN model_override TEXT,
+    ADD COLUMN thinking_level_override TEXT,
+    ADD COLUMN service_tier_override TEXT,
+    ADD COLUMN tier_id UUID,
+    -- M-351 parked marker: a use_tiers enqueue with no free provider slot
+    -- parks the row as queued with no runtime.
+    ADD COLUMN tier_parked BOOLEAN NOT NULL DEFAULT FALSE;
+
+ALTER TABLE agent_task_queue
+    DROP CONSTRAINT IF EXISTS agent_task_queue_active_requires_runtime;
+ALTER TABLE agent_task_queue
+    ADD CONSTRAINT agent_task_queue_active_requires_runtime
+    CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL
+           OR (status = 'queued' AND tier_parked))
+    NOT VALID;
```

```diff
--- a/server/migrations/691_agent_use_tiers.down.sql
+++ b/server/migrations/691_agent_use_tiers.down.sql
+ALTER TABLE agent_task_queue
+    DROP CONSTRAINT IF EXISTS agent_task_queue_active_requires_runtime;
+ALTER TABLE agent_task_queue
+    ADD CONSTRAINT agent_task_queue_active_requires_runtime
+    CHECK (runtime_id IS NOT NULL OR completed_at IS NOT NULL)
+    NOT VALID;
+
+ALTER TABLE agent_task_queue
+    DROP COLUMN IF EXISTS tier_parked;
+ALTER TABLE agent_task_queue
+    DROP COLUMN IF EXISTS tier_id,
+    DROP COLUMN IF EXISTS service_tier_override,
+    DROP COLUMN IF EXISTS thinking_level_override,
+    DROP COLUMN IF EXISTS model_override;
+
+DROP TABLE IF EXISTS agent_tier;
+
+ALTER TABLE agent
+    DROP COLUMN IF EXISTS use_tiers;
```

## Your review (≤250 words)

1. Walk the `down` script against a database that already contains parked rows (`status='queued'`, `runtime_id IS NULL`, `tier_parked=true`). State exactly where it fails or leaves the database wrong, and why the ordering of statements matters.
2. Identify the one irreversible loss in this pair (state which data and which statement destroys it), if any.
3. Give the concrete fix (ordered statements or a pre-step) and one test that proves the round trip on a non-empty database.
