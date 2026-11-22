package handler

// M-500: the ask tool's answer must reach the agent (and stay readable).
//
//  1. An answer that arrives while a comment trigger already holds the pending
//     slot is recorded (not 409'd away) and the pending run carries the
//     handoff, so its prompt renders [ANSWER].
//  2. The answer is mirrored into the issue thread as a system comment.
//  3. The asking agent may read its own card (with the chosen answers and
//     provenance); anyone else is refused with a clear message.
//  4. Escalation never fires for an already-answered card.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func answerAsHuman(t *testing.T, cardID string, answers map[string]any) map[string]any {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/question-cards/"+cardID+"/answer", answers)
	req.Header.Set("X-Actor-Source", "")
	var resp map[string]any
	testutil.Call(t, testHandler.AnswerQuestionCard,
		withURLParams(req, "id", testWorkspaceID, "cardId", cardID),
	).Want(http.StatusOK).JSON(&resp)
	return resp
}

// The Owner answers while a comment-triggered run for the same agent is
// already queued: the answer lands, the pending run carries it, and the
// thread keeps a copy.
func TestAnswerWhileCommentTriggerPendingAttachesHandoff(t *testing.T) {
	f := newAskPlanFixture(t)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1 AND author_type = 'system'`, f.issueID)

	// The parked run was asked from a thread; the card inherits it (M-327).
	askThread := dbfx.Comment(t, f.issueID, "please decide the release day")
	dbfx.Exec(t, `UPDATE agent_task_queue SET trigger_comment_id = $2 WHERE id = $1`, f.taskID, askThread)

	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	// A comment trigger lands while the park waits and takes the pending slot.
	// It shares the parked run's thread: the pending slot is per
	// (issue, agent, tier, thread), so only a same-thread pending run
	// collides with the resume enqueue (a different thread correctly
	// coexists as its own run). This is also the M-500 bug #4 shape — the
	// same trigger comment re-delivered as a new run after the answer.
	pendingTaskID := dbfx.Task(t, f.agentID, testutil.Cols{
		"issue_id":           f.issueID,
		"runtime_id":         f.runtimeID,
		"status":             "queued",
		"trigger_comment_id": askThread,
	})

	resp := answerAsHuman(t, ask.CardID, map[string]any{"answers": []map[string]any{{"selected": []int{0}}}})
	resumeID, _ := resp["resume_task"].(string)
	if resumeID != pendingTaskID {
		t.Fatalf("resume_task = %q, want the already-queued run %q: the answer must ride the pending run, not strand", resumeID, pendingTaskID)
	}

	var kind, refID, note string
	dbfx.QueryRow(t, `SELECT trigger_evidence_kind, trigger_evidence_ref_id::text, COALESCE(handoff_note, '') FROM agent_task_queue WHERE id = $1`, pendingTaskID).Scan(&kind, &refID, &note)
	if kind != "question_answer" || refID != ask.CardID {
		t.Fatalf("pending run evidence = %q/%q, want question_answer/%s", kind, refID, ask.CardID)
	}
	if !strings.Contains(note, "Thursday") {
		t.Fatalf("pending run handoff must carry the chosen label, got:\n%s", note)
	}

	// The card is answered with the chosen answers stored.
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_question_card WHERE id = $1`, ask.CardID).Scan(&status)
	if status != "answered" {
		t.Fatalf("card status = %q, want answered", status)
	}

	// The thread mirror names the decision and hangs under the asking thread.
	var mirror, parent string
	dbfx.QueryRow(t, `SELECT content, COALESCE(parent_id::text, '') FROM comment WHERE issue_id = $1 AND author_type = 'system' AND content LIKE '%answered this question card%'`, f.issueID).Scan(&mirror, &parent)
	if !strings.Contains(mirror, "Thursday") || !strings.Contains(mirror, ask.CardID) {
		t.Fatalf("thread mirror must name the chosen label and the card, got:\n%s", mirror)
	}
	if parent != askThread {
		t.Fatalf("thread mirror parent = %q, want the asking thread %q", parent, askThread)
	}
}

// The asking agent reads its own card back — questions while open, answers and
// provenance once answered.
func TestAskingAgentCanReadOwnCard(t *testing.T) {
	f := newAskPlanFixture(t)
	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	askerReq := func(method string) *http.Request {
		req := newRequest(method, "/api/question-cards/"+ask.CardID, nil)
		req.Header.Set("X-Task-ID", f.taskID)
		req.Header.Set("X-Agent-ID", f.agentID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("X-Actor-Source", "task_token")
		return withURLParams(req, "cardId", ask.CardID)
	}

	var open cardResponse
	testutil.Call(t, testHandler.GetQuestionCardForLead, askerReq(http.MethodGet)).Want(http.StatusOK).JSON(&open)
	if open.ID != ask.CardID || open.Status != "open" {
		t.Fatalf("asker read = %#v, want the open card %s", open, ask.CardID)
	}

	answerAsHuman(t, ask.CardID, map[string]any{"answers": []map[string]any{{"selected": []int{0}}}})

	var answered cardResponse
	testutil.Call(t, testHandler.GetQuestionCardForLead, askerReq(http.MethodGet)).Want(http.StatusOK).JSON(&answered)
	if answered.Status != "answered" {
		t.Fatalf("asker read status = %q, want answered", answered.Status)
	}
	if len(answered.Answers) == 0 || string(answered.Answers) == "null" {
		t.Fatalf("asker read must include the chosen answers, got %#v", answered)
	}
	if answered.AnsweredByType == nil || *answered.AnsweredByType != "member" {
		t.Fatalf("asker read must say the Owner decided, got %#v", answered.AnsweredByType)
	}
	if answered.AnsweredAt == nil || *answered.AnsweredAt == "" {
		t.Fatalf("asker read must say when the answer landed")
	}
}

// An agent that neither asked nor leads gets a refusal that says who may read,
// not a bare permission denied.
func TestUnrelatedAgentReadRefusedWithClearMessage(t *testing.T) {
	f := newAskPlanFixture(t)
	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	otherAgentID := dbfx.Agent(t, "M-500 Bystander Agent", f.runtimeID)
	otherTaskID := dbfx.Task(t, otherAgentID, testutil.Cols{
		"issue_id":   f.issueID,
		"runtime_id": f.runtimeID,
		"status":     "running",
	})
	req := newRequest(http.MethodGet, "/api/question-cards/"+ask.CardID, nil)
	req.Header.Set("X-Task-ID", otherTaskID)
	req.Header.Set("X-Agent-ID", otherAgentID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Actor-Source", "task_token")
	w := testutil.Call(t, testHandler.GetQuestionCardForLead,
		withURLParams(req, "cardId", ask.CardID),
	).Want(http.StatusForbidden)
	if body := w.Body.String(); !strings.Contains(body, "only the asking agent") {
		t.Fatalf("refusal body = %s, want it to name who may read", body)
	}
}

// An answered card is never escalated: no lead run, no escalation comment.
func TestEscalationSkipsAnsweredCard(t *testing.T) {
	f := newAskPlanFixture(t)
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1 AND author_type = 'system'`, f.issueID)

	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)
	answerAsHuman(t, ask.CardID, map[string]any{"answers": []map[string]any{{"selected": []int{0}}}})

	done, err := testHandler.TaskService.EscalateParkedAskPlanTask(context.Background(), util.MustParseUUID(f.taskID), 300, "built-in default")
	if err != nil {
		t.Fatalf("escalate answered park: %v", err)
	}
	if done {
		t.Fatalf("escalate answered park = true, want false: the answer already resumed the run")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%Ask/plan escalation%'`, f.issueID); n != 0 {
		t.Fatalf("escalation comments = %d, want 0 for an answered card", n)
	}
}
