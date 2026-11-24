package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestSpikeModelFreeRoleEnqueueAndClaim is the cheap spike that gates slices 2
// and 3: a Role-shaped agent row (runtime NULL, model empty) must survive the
// real enqueue -> claim path when its model/runtime are stamped from a tier.
// If this fails, the agent-backed Role design is not viable and shared memory
// and the resolver slices must not be built.
func TestSpikeModelFreeRoleEnqueueAndClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	runtimeID := dbfx.Runtime(t, "m501-spike-runtime", testutil.Cols{"provider": "codebuddy"})
	// This is the shape a Role row has: no runtime binding, no model, no
	// account. use_tiers and a role make the existing resolver stamp a real
	// runtime/tier/model onto the task at enqueue time.
	agentID := dbfx.Agent(t, "m501-spike-model-free-role", "", testutil.Cols{
		"model":     "",
		"use_tiers": true,
		"role":      "engineer",
		"subtype":   "role",
		"tier_role": "engineer",
	})
	var tierID string
	dbfx.QueryRow(t, `
		INSERT INTO model_tier (workspace_id, name, idx, runtime_id, provider, model, provider_slots, enabled, roles)
		VALUES ($1, 'm501-spike-tier', 0, $2, 'codebuddy', 'm501-spike-model', 1, true, ARRAY['engineer']::text[])
		RETURNING id
	`, testWorkspaceID, runtimeID).Scan(&tierID)
	dbfx.Exec(t, `INSERT INTO model_tier_role (workspace_id, tier_id, role, idx) VALUES ($1, $2, 'engineer', 0)`, testWorkspaceID, tierID)
	dbfx.Exec(t, `INSERT INTO agent_tier (agent_id, tier_id) VALUES ($1, $2)`, agentID, tierID)

	issueID := dbfx.Issue(t, "m501 spike: model-free role", testutil.Cols{
		"assignee_type": "agent", "assignee_id": agentID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	task, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, issue, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("enqueue model-free Role: %v", err)
	}
	if !task.RuntimeID.Valid || uuidToString(task.RuntimeID) != runtimeID {
		t.Fatalf("enqueue did not stamp a runtime: %+v", task.RuntimeID)
	}
	if !task.TierID.Valid || uuidToString(task.TierID) != tierID {
		t.Fatalf("enqueue did not stamp a tier: %+v", task.TierID)
	}
	if !task.ModelOverride.Valid || task.ModelOverride.String != "m501-spike-model" {
		t.Fatalf("enqueue did not stamp a model override: %+v", task.ModelOverride)
	}

	// The Role row itself stays model-free; only the task carries the stamp.
	var runtimeNull bool
	var modelText string
	dbfx.QueryRow(t, `SELECT runtime_id IS NULL, COALESCE(model, '') FROM agent WHERE id = $1`, agentID).Scan(&runtimeNull, &modelText)
	if !runtimeNull || modelText != "" {
		t.Fatalf("Role row changed: runtime_null=%v model=%q", runtimeNull, modelText)
	}

	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatalf("claim model-free Role task: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != uuidToString(task.ID) {
		t.Fatalf("claimed task = %+v, want %s", claimed, uuidToString(task.ID))
	}

	// Delivery must also succeed: the task tier stamp exempts the NULL agent
	// runtime from the claim-time rebind fence and supplies the model.
	rt, err := testHandler.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID: parseUUID(runtimeID), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
	_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, rt, runtimeID, testWorkspaceID)
	if failure != nil {
		t.Fatalf("deliver model-free Role task: %+v", failure)
	}

	// Adversarial: the same model-free row with use_tiers off must be refused
	// at enqueue — there is no runtime or model to route it.
	dbfx.Exec(t, `UPDATE agent SET use_tiers = false WHERE id = $1`, agentID)
	issueID2 := dbfx.Issue(t, "m501 spike: model-free role without tiers", testutil.Cols{
		"assignee_type": "agent", "assignee_id": agentID,
	})
	issue2, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID2))
	if err != nil {
		t.Fatalf("load second issue: %v", err)
	}
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, issue2, parseUUID(testUserID)); err == nil {
		t.Fatal("model-free Role with use_tiers=false was enqueued, want refusal")
	}
}
