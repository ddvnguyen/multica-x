# Task V4 — code review: tier resolver race

You are reviewing a real merged pull-request diff from the `multica-x` repo (M-351: enqueue-time tier resolver). Do not write code files; answer in chat.

## Context

`resolveAgentTier` runs at enqueue and stamps `runtime_id` + model overrides on a queued row. A tier declares `provider_slots` (max simultaneous sessions on its runtime). The claim path later runs `ClaimAgentTask`, which does *not* re-check capacity.

## Diff excerpt (`server/internal/service/tier_resolve.go`, new file)

```go
func (s *TaskService) resolveAgentTier(ctx context.Context, agent db.Agent) (tierResolution, error) {
	selections, err := s.Queries.ListAgentTiers(ctx, agent.ID)
	...
	for _, t := range enabled {
		rt, ok := runtimes[util.UUIDToString(t.RuntimeID)]
		if !t.RuntimeID.Valid || !ok { continue }
		if !isRuntimeClaimable(rt) { continue }
		running, err := s.Queries.CountActiveTasksByRuntime(ctx, rt.ID)  // one query per tier
		if err != nil { return none, err }
		slots := int64(t.ProviderSlots)
		if slots < 1 { slots = 1 }
		if running >= slots { continue }
		return tierResolution{ runtimeID: rt.ID, tierID: t.ID, hasCapacity: true }, nil
	}
	return none, errNoTierCapacity
}
```

## Your review (≤250 words)

1. Name the concurrency defect: describe two concurrent enqueues for the same agent and the exact interleaving that over-subscribes a runtime, quoting the two statements involved.
2. Say what the fix belongs to — a cheaper read, a transactional reservation at enqueue, or a re-check at claim — and why the neighbours you are *not* changing (`ClaimAgentTask`, the capacity-freed sweep, the parked-row promotion) do or do not already close it.
3. Give one deterministic test (no sleeps, no flake) that would fail before the fix and pass after, naming the seam to inject.
4. Note any non-concurrency issue in the loop that a reviewer should still raise (cost, N+1, error handling).
