package handler

// M-333: ask/plan inbox rows are archived when their card/plan resolves —
// on answer/verdict, on cancel/withdraw (through the task:cancelled listener),
// and lazily on list for rows created before their resolution was archived.

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// countAskInboxRows counts one ask/plan inbox type's rows for an issue in a
// given archived state. The fixture owns these rows, so counts are scoped to
// the issue, never to the workspace.
func countAskInboxRows(t *testing.T, issueID, typ string, archived bool) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND type = $2 AND archived = $3`, issueID, typ, archived)
}

// captureBatchArchived subscribes the handler's bus to inbox:batch-archived
// and appends every event to the returned slice. The bus dispatches
// synchronously, so a handler call's events are observable right after.
func captureBatchArchived() *[]events.Event {
	var batch []events.Event
	testHandler.Bus.Subscribe(protocol.EventInboxBatchArchived, func(e events.Event) {
		batch = append(batch, e)
	})
	return &batch
}

func batchRecipientCounts(t *testing.T, batch []events.Event) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, e := range batch {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			t.Fatalf("batch-archived payload = %#v, want a map", e.Payload)
		}
		recipientID, _ := payload["recipient_id"].(string)
		count, _ := payload["count"].(int64)
		if recipientID == "" {
			t.Fatalf("batch-archived event without recipient_id: %#v", payload)
		}
		counts[recipientID] += count
	}
	return counts
}

// TestAnswerQuestionCardArchivesAskInboxForAllMembers: the answer closes the
// card, so every member's agent_question row for the issue goes to the archive
// without a manual dismiss, and each member's clients get one
// inbox:batch-archived with their own count. The ask also leaves an escalation
// row (details carry only the parked task id) on the same issue; the answer
// resolves that one too, because the ask it points at is now answered.
func TestAnswerQuestionCardArchivesAskInboxForAllMembers(t *testing.T) {
	f := newAskPlanFixture(t)
	secondMember := createTestUserAndMember(t, "member")

	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	// Both members hold an active agent_question row (one per member, from
	// deliverAskPlanInbox), plus the escalation row for the first member.
	if n := countAskInboxRows(t, f.issueID, "agent_question", false); n != 2 {
		t.Fatalf("active agent_question rows = %d, want 2 (one per member)", n)
	}
	dbfx.Insert(t, "inbox_item", testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"recipient_type": "member",
		"recipient_id":   testUserID,
		"type":           "agent_question",
		"severity":       "action_required",
		"issue_id":       f.issueID,
		"title":          "Ask/plan timeout: escalation row",
		"details":        testutil.Raw(`'{"task_id":"` + f.taskID + `"}'::jsonb`),
	})

	batch := captureBatchArchived()
	answerReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/question-cards/"+ask.CardID+"/answer", map[string]any{
		"answers": []map[string]any{{"selected": []int{0}}},
	})
	answerReq.Header.Set("X-Actor-Source", "") // human credential, not a task token
	testutil.Call(t, testHandler.AnswerQuestionCard,
		withURLParams(answerReq, "id", testWorkspaceID, "cardId", ask.CardID),
	).Want(http.StatusOK)

	// Every ask row for the issue is archived, escalation row included.
	if n := countAskInboxRows(t, f.issueID, "agent_question", false); n != 0 {
		t.Fatalf("active agent_question rows after answer = %d, want 0", n)
	}
	if n := countAskInboxRows(t, f.issueID, "agent_question", true); n != 3 {
		t.Fatalf("archived agent_question rows after answer = %d, want 3", n)
	}

	// One event per member, each naming its own recipient and count.
	counts := batchRecipientCounts(t, *batch)
	if counts[testUserID] != 2 {
		t.Fatalf("batch-archived count for the first member = %d, want 2", counts[testUserID])
	}
	if counts[secondMember] != 1 {
		t.Fatalf("batch-archived count for the second member = %d, want 1", counts[secondMember])
	}
}

// TestAnswerQuestionCardArchivesOnlyTheResolvedCard: two asks parked on one
// issue (each on its own task). Answering the first card must archive only the
// first card's rows; the second ask's rows stay active.
func TestAnswerQuestionCardArchivesOnlyTheResolvedCard(t *testing.T) {
	f := newAskPlanFixture(t)
	secondTask := dbfx.Task(t, f.agentID, testutil.Cols{
		"issue_id":   f.issueID,
		"runtime_id": f.runtimeID,
		"status":     "running",
	})
	dbfx.Cleanup(t, `DELETE FROM agent_question_card WHERE task_id = $1`, secondTask)

	var firstAsk askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&firstAsk)

	var secondAsk askResponse
	secondAskReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+secondTask+"/ask", validAskBody())
	secondAskReq.Header.Set("X-Task-ID", secondTask)
	secondAskReq.Header.Set("X-Agent-ID", f.agentID)
	secondAskReq.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.SubmitAsk,
		withURLParams(secondAskReq, "id", testWorkspaceID, "taskId", secondTask),
	).Want(http.StatusAccepted).JSON(&secondAsk)

	if n := countAskInboxRows(t, f.issueID, "agent_question", false); n != 2 {
		t.Fatalf("active agent_question rows = %d, want 2 (one per card per member)", n)
	}

	answerReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/question-cards/"+firstAsk.CardID+"/answer", map[string]any{
		"answers": []map[string]any{{"selected": []int{0}}},
	})
	answerReq.Header.Set("X-Actor-Source", "")
	testutil.Call(t, testHandler.AnswerQuestionCard,
		withURLParams(answerReq, "id", testWorkspaceID, "cardId", firstAsk.CardID),
	).Want(http.StatusOK)

	// Only the answered card's row left the active set; the second card's row
	// stays until its own resolution.
	if n := countAskInboxRows(t, f.issueID, "agent_question", false); n != 1 {
		t.Fatalf("active agent_question rows after answering one of two = %d, want 1", n)
	}
	if n := countAskInboxRows(t, f.issueID, "agent_question", true); n != 1 {
		t.Fatalf("archived agent_question rows after answering one of two = %d, want 1", n)
	}
}

// TestListInboxLazilyArchivesResolvedAskRows: a row created before its card's
// resolution was archived (older deploy) resolves the moment the member lists
// the inbox — the response never shows it, and the row lands in the archive.
func TestListInboxLazilyArchivesResolvedAskRows(t *testing.T) {
	f := newAskPlanFixture(t)
	var ask askResponse
	testutil.Call(t, testHandler.SubmitAsk,
		agentAskRequest(t, f, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tasks/"+f.taskID+"/ask", validAskBody()),
	).Want(http.StatusAccepted).JSON(&ask)

	// Simulate the pre-fix state: the card resolved without its archive event.
	dbfx.Exec(t, `UPDATE agent_question_card SET status='answered', answered_at=now() WHERE id=$1`, ask.CardID)
	if n := countAskInboxRows(t, f.issueID, "agent_question", false); n != 1 {
		t.Fatalf("pre-list active rows = %d, want 1", n)
	}

	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox", testWorkspaceID),
	).Want(http.StatusOK).JSON(&items)
	for _, item := range items {
		if item.IssueID != nil && *item.IssueID == f.issueID && item.Type == "agent_question" {
			t.Fatalf("resolved ask still listed: %+v", item)
		}
	}
	if n := countAskInboxRows(t, f.issueID, "agent_question", true); n != 1 {
		t.Fatalf("archived rows after list = %d, want 1", n)
	}

	// Listing again is a no-op: the row is already archived.
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox", testWorkspaceID),
	).Want(http.StatusOK).JSON(&items)
}

// TestReviewPlanArchivesPlanInboxRows: a verdict moves the plan out of
// in_review, so its plan_review inbox row is archived for every member and the
// member's clients get the batch-archived event.
func TestReviewPlanArchivesPlanInboxRows(t *testing.T) {
	f := newAskPlanFixture(t)
	secondMember := createTestUserAndMember(t, "member")
	planID, rev := submittedPlan(t, f)

	if n := countAskInboxRows(t, f.issueID, "plan_review", false); n != 2 {
		t.Fatalf("active plan_review rows = %d, want 2 (one per member)", n)
	}

	batch := captureBatchArchived()
	verdictReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/verdict", map[string]any{
		"revision": rev, "verdict": "approve",
	})
	testutil.Call(t, testHandler.ReviewPlan,
		withURLParams(verdictReq, "id", testWorkspaceID, "planId", planID),
	).Want(http.StatusOK)

	if n := countAskInboxRows(t, f.issueID, "plan_review", false); n != 0 {
		t.Fatalf("active plan_review rows after verdict = %d, want 0", n)
	}
	if n := countAskInboxRows(t, f.issueID, "plan_review", true); n != 2 {
		t.Fatalf("archived plan_review rows after verdict = %d, want 2", n)
	}
	counts := batchRecipientCounts(t, *batch)
	if counts[testUserID] != 1 || counts[secondMember] != 1 {
		t.Fatalf("batch-archived counts = %v, want one for each member", counts)
	}
}
