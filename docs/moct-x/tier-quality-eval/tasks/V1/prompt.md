# Task V1 — code review: tier-stamped claim fence

You are reviewing a real merged pull-request diff from the `multica-x` repo (M-351: agent "Use tiers" + tier resolver + claim fence). Do not write code files; answer in chat.

## Context

`agent_task_queue` rows are claimed by a runtime. Before this change the claim candidate query required `a.runtime_id = atq.runtime_id` — a row is only offered to the runtime its agent is currently bound to. M-351 adds an enqueue-time stamp (`atq.tier_id`) so a task resolved to a tier can be claimed by any runtime that hosts that tier.

## Diff excerpt (`server/pkg/db/generated/agent.sql.go`)

```diff
@@ ListQueuedClaimCandidatesByRuntime
       FROM agent a
       JOIN agent_runtime r ON r.id = atq.runtime_id
       WHERE a.id = atq.agent_id
-        AND a.runtime_id = atq.runtime_id
+        -- M-351: a tier-stamped row resolved its runtime at enqueue, so the
+        -- agent's current single runtime must not fence it out. Owner and
+        -- visibility checks below still apply.
+        AND (a.runtime_id = atq.runtime_id OR atq.tier_id IS NOT NULL)
         AND (r.visibility = 'public' OR ...)
```

The same edit is applied to `ListQueuedClaimCandidatesByRuntimes`. Two adjacent strict-fence callers were left unchanged: the *dispatched reclaim* path and `ClaimAgentTask` itself; the PR's own test asserts `"dispatched reclaim must keep the strict fence"`.

## Your review (≤250 words)

1. Name the single most important defect or regression risk this carve-out introduces, with the exact column/condition that causes it.
2. Describe the blast radius: what can now happen to a task row that could not happen before (be concrete about rebind / agent-runtime changes / tier deletion or disable).
3. Give one specific test — name the scenario and the assertion — that would catch it. Say whether it needs only the two rows above or a live DB.
4. If you believe the change is safe as written, say so explicitly and justify it against point 2 instead.
