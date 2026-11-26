package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// M-597 G2. An agent actor's assign carries no member id — memberActorUserID is
// deliberately invalid for actor_type=agent so an agent id can never become an
// accountable human — so the Role pick reached authorizeRoleForRun with no
// requester and the enqueue died with "role is not accessible to this member".
// The write boundary had already authorised the very same assignment against the
// resolved human originator (issue.go validateAssigneePair → canInvokeAgent with
// invokeOriginatorFromRequest), so the API reported a run that could never be
// created.
//
// These tests drive the real UpdateIssue handler as a task-token agent actor, so
// they cover the wiring (resolveActor → dispatchIssueRun → enqueue), not only
// the service helper.

// m597AgentRun seeds an agent actor plus one running task of its own. The task's
// originator is the human the agent acts for; "" leaves the run unattributed —
// the schedule/webhook shape that must stay denied.
func m597AgentRun(t *testing.T, name, originator string) (agentID, taskID string) {
	t.Helper()
	agentID = createHandlerTestAgent(t, name, nil)
	own := dbfx.Issue(t, name+" issue", testutil.Cols{})
	cols := testutil.Cols{
		"issue_id":   own,
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
	}
	if originator != "" {
		cols["originator_user_id"] = originator
		cols["accountable_user_id"] = originator
	}
	return agentID, dbfx.Task(t, agentID, cols)
}

// m597RoleAssignTarget is an unassigned issue to hand to the Role.
func m597RoleAssignTarget(t *testing.T, title string) string {
	t.Helper()
	return dbfx.Issue(t, title, testutil.Cols{"status": "todo"})
}

func m597AssignAsAgent(t *testing.T, issueID, roleID, agentID, taskID string) *testutil.Response {
	t.Helper()
	req := agentRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   roleID,
	}, agentID, taskID)
	return testutil.Call(t, testHandler.UpdateIssue, withURLParam(req, "id", issueID))
}

// An agent actor delegating to a Role on behalf of the owner must resolve the
// Role's tier and enqueue the run, exactly as the member path does.
func TestAgentActorAssignToRoleQueuesRun(t *testing.T) {
	fix := setupRoleRun(t, 1, true)
	coordinator, sourceTask := m597AgentRun(t, roleTestName("M-597 assign coordinator"), testUserID)
	target := m597RoleAssignTarget(t, "M-597 role assign target "+roleTestName(""))

	var resp IssueResponse
	m597AssignAsAgent(t, target, fix.roleID, coordinator, sourceTask).Want(http.StatusOK).JSON(&resp)
	wantRunOutcome(t, resp.RunOutcome, "agent", fix.roleID, DispatchQueued, ReasonQueued)

	if got := taskCountFor(t, target, fix.roleID); got != 1 {
		t.Fatalf("tasks = %d, want the assign to enqueue one Role run", got)
	}
	var tierID, runtimeID string
	dbfx.QueryRow(t, `SELECT COALESCE(tier_id::text, ''), COALESCE(runtime_id::text, '')
		FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, target, fix.roleID).Scan(&tierID, &runtimeID)
	if tierID != fix.tierA || runtimeID != fix.rtA {
		t.Fatalf("queued run tier=%q runtime=%q, want the Role's first tier %q/%q", tierID, runtimeID, fix.tierA, fix.rtA)
	}
}

// The same delegation from an unattributed run stays denied: with no human at
// the top of the chain there is nobody to authorise, so the write is refused
// and nothing is queued or reassigned.
func TestAgentActorAssignToRoleWithoutOriginatorIsDenied(t *testing.T) {
	fix := setupRoleRun(t, 1, true)
	coordinator, sourceTask := m597AgentRun(t, roleTestName("M-597 unattributed coordinator"), "")
	target := m597RoleAssignTarget(t, "M-597 unattributed assign target "+roleTestName(""))

	m597AssignAsAgent(t, target, fix.roleID, coordinator, sourceTask).Want(http.StatusForbidden)

	if got := taskCountFor(t, target, fix.roleID); got != 0 {
		t.Fatalf("tasks = %d, want an unattributed agent assign to queue nothing", got)
	}
	var assigneeID string
	dbfx.QueryRow(t, `SELECT COALESCE(assignee_id::text, '') FROM issue WHERE id = $1`, target).Scan(&assigneeID)
	if assigneeID == fix.roleID {
		t.Fatal("a refused Role assignment must not be written to the issue")
	}
}
