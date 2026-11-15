package handler

import (
	"context"
	"net/http"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestBuildClaimedTaskResponseDeliversTierStampedTaskAfterRebind pins the
// M-351 delivery gate: a task carrying a tier stamp resolved its runtime at
// enqueue, so the agent's current single runtime must not refuse delivery —
// while a plain (unstamped) task on a rebound agent still fails with
// error_agent_runtime_changed. Owner and visibility checks are unchanged.
func TestBuildClaimedTaskResponseDeliversTierStampedTaskAfterRebind(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	oldRuntimeID := createClaimReclaimRuntime(t, ctx, "Tier delivery old runtime")
	newRuntimeID := createClaimReclaimRuntime(t, ctx, "Tier delivery new runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, oldRuntimeID, "Tier delivery agent")

	// Stamp the task as tier-resolved (M-351): runtime + model override + tier.
	var tierID string
	dbfx.QueryRow(t, `
		INSERT INTO model_tier (workspace_id, name, idx, runtime_id, provider, model, provider_slots, enabled)
		VALUES ($1, 'tier-delivery-probe', 0, $2, 'codebuddy', 'tier-probe-model', 1, true)
		RETURNING id
	`, testWorkspaceID, newRuntimeID).Scan(&tierID)
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM model_tier WHERE id = $1`, tierID) })

	taskID := seedQueuedIssueTask(t, ctx, agentID, newRuntimeID, issueID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET model_override = 'tier-probe-model', tier_id = $1 WHERE id = $2`, tierID, taskID)

	task, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(newRuntimeID))
	if err != nil {
		t.Fatalf("claim task: %v", err)
	}
	if task == nil || uuidToString(task.ID) != taskID {
		t.Fatalf("claimed task = %+v, want %s", task, taskID)
	}
	// Agent stays bound to the OLD runtime: without the tier carve-out the
	// delivery gate would refuse with error_agent_runtime_changed.
	dbfx.Exec(t, `UPDATE agent SET use_tiers = true WHERE id = $1`, agentID)

	runtime, err := testHandler.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          parseUUID(newRuntimeID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load new runtime: %v", err)
	}
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+newRuntimeID+"/tasks/claim", nil)
	_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(
		req, task, runtime, newRuntimeID, testWorkspaceID,
	)
	if failure != nil {
		t.Fatalf("tier-stamped delivery failed: %+v (want success)", failure)
	}
}
