package handler

// M-332: the project lead's answer path for an escalated ask card.
//
// The ladder is Owner → (timeout) → lead, once → Owner. These tests pin the
// gates on the lead's door: only the issue project's agent lead, only with a
// task token, only after the card escalated, and the same first-answer-wins CAS
// the human route uses. The sweeper half lives in
// cmd/server/askplan_timeout_sweeper_test.go.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type leadAnswerFixture struct {
	askPlanFixture
	projectID   string
	leadAgentID string
	leadTaskID  string
	cardID      string
}

// newLeadAnswerFixture parks an ask from one agent, makes a SECOND agent the
// project lead (a distinct agent so the lead's own escalated run cannot collide
// with the parked run's resume), and hands back the lead's task id.
func newLeadAnswerFixture(t *testing.T) leadAnswerFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newAskPlanFixture(t)
	leadAgentID := dbfx.Agent(t, "M-332 Lead Agent", f.runtimeID)
	leadTaskID := dbfx.Task(t, leadAgentID, testutil.Cols{
		"issue_id":   f.issueID,
		"runtime_id": f.runtimeID,
		"status":     "running",
	})
	projectID := dbfx.Project(t, "M-332 lead project", testutil.Cols{
		"lead_type": "agent",
		"lead_id":   leadAgentID,
	})
	dbfx.Exec(t, `UPDATE issue SET project_id = $2 WHERE id = $1`, f.issueID, projectID)

	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	return leadAnswerFixture{
		askPlanFixture: f,
		projectID:      projectID,
		leadAgentID:    leadAgentID,
		leadTaskID:     leadTaskID,
		cardID:         ask.CardID,
	}
}

// leadCardRequest is the request the router produces for the non-workspace-scoped
// card routes: the task token identifies both the agent and its workspace (the
// auth middleware copies the token's workspace into X-Workspace-ID).
func leadCardRequest(t *testing.T, f leadAnswerFixture, method string, body any) *http.Request {
	t.Helper()
	req := newRequest(method, "/api/question-cards/"+f.cardID, body)
	req.Header.Set("X-Task-ID", f.leadTaskID)
	req.Header.Set("X-Agent-ID", f.leadAgentID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Actor-Source", "task_token")
	return withURLParams(req, "cardId", f.cardID)
}

func markCardEscalated(t *testing.T, cardID string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE agent_question_card SET escalated_at = now() WHERE id = $1`, cardID)
}

func leadCardAnswerBody() map[string]any {
	return map[string]any{"answers": []map[string]any{{"selected": []int{1}}}}
}

// The lead answers after the escalation: the card is answered as the LEAD, the
// original parked run resumes, and the handoff note says who decided.
func TestLeadAnswerQuestionCardRecordsTheLeadAndResumesTheRun(t *testing.T) {
	f := newLeadAnswerFixture(t)
	markCardEscalated(t, f.cardID)

	var resp map[string]any
	testutil.Call(t, testHandler.LeadAnswerQuestionCard,
		leadCardRequest(t, f, http.MethodPost, leadCardAnswerBody()),
	).Want(http.StatusOK).JSON(&resp)

	var status, byType, answeredBy string
	dbfx.QueryRow(t, `SELECT status, answered_by_type, answered_by::text FROM agent_question_card WHERE id = $1`, f.cardID).
		Scan(&status, &byType, &answeredBy)
	if status != "answered" || byType != "agent" || answeredBy != f.leadAgentID {
		t.Fatalf("card = %q by %q/%q, want answered by agent %q", status, byType, answeredBy, f.leadAgentID)
	}
	resumeID, _ := resp["resume_task"].(string)
	if resumeID == "" {
		t.Fatalf("no resume task in the response: %#v", resp)
	}
	var rerunOf, note string
	dbfx.QueryRow(t, `SELECT rerun_of_task_id::text, COALESCE(handoff_note, '') FROM agent_task_queue WHERE id = $1`, resumeID).
		Scan(&rerunOf, &note)
	if rerunOf != f.taskID {
		t.Fatalf("resume rerun_of = %q, want the parked task %q", rerunOf, f.taskID)
	}
	if !strings.Contains(note, "M-332 Lead Agent") {
		t.Fatalf("handoff note must name the lead, got:\n%s", note)
	}
	if !strings.Contains(note, "on the Owner's behalf") {
		t.Fatalf("handoff note must say the answer is on the Owner's behalf, got:\n%s", note)
	}
}

// A task token that is not the project lead's is refused, and refused before
// anything is written: the asking agent cannot answer its own card, not even
// after the escalation.
func TestLeadAnswerQuestionCardRefusesNonLeadAgent(t *testing.T) {
	f := newLeadAnswerFixture(t)
	markCardEscalated(t, f.cardID)

	// The parked agent's own token (the asker), on the same route.
	req := leadCardRequest(t, f, http.MethodPost, leadCardAnswerBody())
	req.Header.Set("X-Task-ID", f.taskID)
	req.Header.Set("X-Agent-ID", f.agentID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, testHandler.LeadAnswerQuestionCard, req).Want(http.StatusForbidden)

	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_question_card WHERE id = $1`, f.cardID).Scan(&status)
	if status != "open" {
		t.Fatalf("card status = %q, want open after a refused answer", status)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id = $1`, f.taskID); n != 0 {
		t.Fatalf("resume tasks = %d, want 0 after a refused answer", n)
	}
}

// Rung 1 is the Owner's: before the escalation the lead's answer is refused with
// the business conflict, not silently accepted.
func TestLeadAnswerQuestionCardRefusesBeforeEscalation(t *testing.T) {
	f := newLeadAnswerFixture(t)
	testutil.Call(t, testHandler.LeadAnswerQuestionCard,
		leadCardRequest(t, f, http.MethodPost, leadCardAnswerBody()),
	).Want(http.StatusConflict)
}

// A member session (no task token) cannot use the lead route at all.
func TestLeadAnswerQuestionCardRefusesHumanSession(t *testing.T) {
	f := newLeadAnswerFixture(t)
	markCardEscalated(t, f.cardID)

	req := newRequest(http.MethodPost, "/api/question-cards/"+f.cardID, leadCardAnswerBody())
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, testHandler.LeadAnswerQuestionCard,
		withURLParams(req, "cardId", f.cardID),
	).Want(http.StatusForbidden)
}

// First answer wins across the two doors: the Owner answers first, the lead's
// answer loses its CAS with card_closed.
func TestLeadAnswerQuestionCardLosesToAnOwnerAnswer(t *testing.T) {
	f := newLeadAnswerFixture(t)
	markCardEscalated(t, f.cardID)

	ownerReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/question-cards/"+f.cardID+"/answer", map[string]any{
		"answers": []map[string]any{{"selected": []int{0}}},
	})
	ownerReq.Header.Set("X-Actor-Source", "")
	testutil.Call(t, testHandler.AnswerQuestionCard,
		withURLParams(ownerReq, "id", testWorkspaceID, "cardId", f.cardID),
	).Want(http.StatusOK)

	req := leadCardRequest(t, f, http.MethodPost, leadCardAnswerBody())
	w := testutil.Call(t, testHandler.LeadAnswerQuestionCard, req).Want(http.StatusConflict)
	if body := w.Body.String(); !strings.Contains(body, "card_closed") {
		t.Fatalf("conflict body = %s, want card_closed", body)
	}
	var byType string
	dbfx.QueryRow(t, `SELECT answered_by_type FROM agent_question_card WHERE id = $1`, f.cardID).Scan(&byType)
	if byType != "member" {
		t.Fatalf("answered_by_type = %q, want member (the Owner's answer stands)", byType)
	}
}

// The lead can read the card it was escalated to, and a non-lead cannot.
func TestGetQuestionCardForLead(t *testing.T) {
	f := newLeadAnswerFixture(t)
	var card cardResponse
	testutil.Call(t, testHandler.GetQuestionCardForLead,
		leadCardRequest(t, f, http.MethodGet, nil),
	).Want(http.StatusOK).JSON(&card)
	if card.ID != f.cardID || card.Status != "open" {
		t.Fatalf("card = %#v, want the open card %s", card, f.cardID)
	}

	req := leadCardRequest(t, f, http.MethodGet, nil)
	req.Header.Set("X-Task-ID", f.taskID)
	req.Header.Set("X-Agent-ID", f.agentID)
	testutil.Call(t, testHandler.GetQuestionCardForLead, req).Want(http.StatusForbidden)
}
