package db

import (
	"strings"
	"testing"
)

// TestClaimFence_TierCarveOutPresent is a DB-free guard on the M-351 claim
// contract: the three queued-claim queries (ClaimAgentTask + the two batch
// candidate selects) must bypass the agent.runtime_id equality for
// tier-stamped rows, while the two dispatched-reclaim queries keep the strict
// fence. Owner and visibility checks are untouched in all five — this test
// only watches the equality carve-out. Full behavioral coverage (a tier row
// claimed off a rebound agent; a plain row still fenced) runs against a live
// DB in CI through the claim path.
func TestClaimFence_TierCarveOutPresent(t *testing.T) {
	queued := map[string]string{
		"ClaimAgentTask":                      claimAgentTask,
		"ListQueuedClaimCandidatesByRuntime":  listQueuedClaimCandidatesByRuntime,
		"ListQueuedClaimCandidatesByRuntimes": listQueuedClaimCandidatesByRuntimes,
	}
	for name, sql := range queued {
		if !strings.Contains(sql, "atq.tier_id IS NOT NULL") {
			t.Errorf("%s: missing tier carve-out", name)
		}
	}
	strict := map[string]string{
		"ReclaimStaleDispatchedTaskForRuntime":   reclaimStaleDispatchedTaskForRuntime,
		"ReclaimStaleDispatchedTasksForRuntimes": reclaimStaleDispatchedTasksForRuntimes,
	}
	for name, sql := range strict {
		if strings.Contains(sql, "atq.tier_id IS NOT NULL") {
			t.Errorf("%s: dispatched reclaim must keep the strict fence", name)
		}
	}
}
