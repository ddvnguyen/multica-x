# Task P1 — plan: tier resolver + claim fence

Write a plan (≤400 words) for adding an enqueue-time tier resolver and the matching claim-fence carve-out to a Go/Postgres service (`multica-x`). The table `model_tier(id, idx, runtime_id, provider_slots, enabled)` and the join `agent_tier(agent_id, tier_id)` already exist; `agent_task_queue` already has `tier_id` and `tier_parked`.

Your plan must contain these headed sections and nothing else:

1. `## Outcome` — one paragraph, what the user sees change.
2. `## Scope` — two short lists: In (files/packages) and Out.
3. `## Acceptance` — three or more checks that a reviewer can run, each with a command or a named test.
4. `## Risks` — two or more concrete failure modes with the mitigation for each.
5. `## Files` — the exact repo paths you would touch, at least three, each with one clause on why.

Plan only; write no code files.
