package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func m209ReviewSetup(t *testing.T) (string, string, string, map[string]any) {
	t.Helper()
	seedTestCatalog(t)
	agent := createHandlerTestAgent(t, "M209 review target", []byte(`{}`))
	project := deliveryProject(t, "M209 review project", agent)
	issue := dbfx.Issue(t, "M209 review answer", testutil.Cols{"project_id": project, "status": "wait_for_decision"})
	t.Cleanup(func() {
		for _, table := range []string{"issue_decision_response", "comment", "agent_task_queue"} {
			_, _ = testPool.Exec(context.Background(), "DELETE FROM "+table+" WHERE issue_id=$1", issue)
		}
	})
	var revision int64
	var requestID string
	dbfx.QueryRow(t, "SELECT revision, gen_random_uuid()::text FROM issue WHERE id=$1", issue).Scan(&revision, &requestID)
	return issue, agent, project, map[string]any{"client_request_id": requestID, "expected_issue_revision": revision, "decision": "Approve", "next_status": "in_progress"}
}

func m209ReviewCall(h *Handler, issue string, body map[string]any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := withURLParam(workflowRequest("POST", "/api/issues/"+issue+"/decision-response", body), "issueId", issue)
	h.DecisionResponse(w, req)
	return w
}

func TestM209ReviewTaskUsesDestinationStep(t *testing.T) {
	issue, _, _, body := m209ReviewSetup(t)
	w := m209ReviewCall(testHandler, issue, body)
	if w.Code != 200 {
		t.Fatalf("initial response %d: %s", w.Code, w.Body.String())
	}
	var step string
	dbfx.QueryRow(t, "SELECT workflow_step FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&step)
	if step != "in_progress" {
		t.Fatalf("queued task workflow_step=%q, want destination in_progress", step)
	}
}

func TestM209ReviewRetrySurvivesArchivedTarget(t *testing.T) {
	issue, agent, _, body := m209ReviewSetup(t)
	w := m209ReviewCall(testHandler, issue, body)
	if w.Code != 200 {
		t.Fatalf("initial response %d: %s", w.Code, w.Body.String())
	}
	dbfx.Exec(t, "UPDATE agent SET archived_at=now() WHERE id=$1", agent)
	retry := m209ReviewCall(testHandler, issue, body)
	if retry.Code != 200 {
		t.Fatalf("matching retry %d: %s; expected original receipt", retry.Code, retry.Body.String())
	}
}

func TestM209ReviewWorkflowTargetChangedBeforeBegin(t *testing.T) {
	issue, _, project, body := m209ReviewSetup(t)
	replacement := createHandlerTestAgent(t, "M209 replacement target", []byte(`{}`))
	h := *testHandler
	h.TxStarter = hookedTxStarter{base: testHandler.TxStarter, hook: func() error {
		setColumnHandoff(t, project, "in_progress", handToAgent(replacement, "Replacement instructions")).Want(200)
		return nil
	}}
	w := m209ReviewCall(&h, issue, body)
	if w.Code == 409 {
		return
	}
	if w.Code != 200 {
		t.Fatalf("unexpected response %d: %s", w.Code, w.Body.String())
	}
	var assignee, queued string
	dbfx.QueryRow(t, "SELECT assignee_id::text FROM issue WHERE id=$1", issue).Scan(&assignee)
	dbfx.QueryRow(t, "SELECT agent_id::text FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&queued)
	if assignee != queued {
		t.Fatalf("committed issue assignee=%s but queued agent=%s", assignee, queued)
	}
}

func TestM209ReviewMemberHandoffRequiresNoTask(t *testing.T) {
	issue, _, project, body := m209ReviewSetup(t)
	setColumnHandoff(t, project, "in_progress", map[string]any{"handler": map[string]any{"type": "member", "id": testUserID}}).Want(200)
	w := m209ReviewCall(testHandler, issue, body)
	if w.Code != 200 {
		t.Fatalf("stable member handoff returned %d: %s; expected receipt/status/comment without task", w.Code, w.Body.String())
	}
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&count)
	if count != 0 {
		t.Fatalf("member handoff created %d tasks", count)
	}
}
