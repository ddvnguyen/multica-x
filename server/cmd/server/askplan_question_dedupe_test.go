package main

// M-362: the task:message question listener must not double-notify a park
// SubmitAsk/SubmitPlan already covered. The handlers write one member row per
// workspace member with the evidence id in details; the listener's subscriber
// row for the same park would be a second trip to the same inbox.
//
//   - member row present (park via the handlers) → the subscriber question
//     event writes nothing.
//   - no member row (a park that bypassed the handlers) → the subscriber
//     row still notifies, exactly once.

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func insertOpenCardWithMemberRow(t *testing.T, issueID string) string {
	t.Helper()
	cardID := uuid.NewString()
	taskID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_question_card (id, workspace_id, issue_id, task_id, agent_id, status, questions)
		VALUES ($1, $2, $3, $4, $5, 'open', '[]'::jsonb)
	`, cardID, testWorkspaceID, issueID, taskID, agentID); err != nil {
		t.Fatalf("insert open card: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_question_card WHERE id = $1`, cardID)
	})
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO inbox_item (id, workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, details)
		VALUES ($1, $2, 'member', $3, 'agent_question', 'action_required', $4, 'ask', $5::jsonb)
	`, uuid.NewString(), testWorkspaceID, testUserID, issueID, fmt.Sprintf(`{"card_id":%q}`, cardID)); err != nil {
		t.Fatalf("insert member row: %v", err)
	}
	return cardID
}

func TestNotification_QuestionDedupe_MemberRowSuppressesSubscriberRow(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	addTestSubscriber(t, issueID, "member", testUserID, "creator")
	insertOpenCardWithMemberRow(t, issueID)

	before := len(inboxItemsForRecipient(t, queries, testUserID))
	publishAgentQuestion(bus, issueID, agentQuestionInput())

	if got := len(inboxItemsForRecipient(t, queries, testUserID)); got != before {
		t.Fatalf("subscriber question wrote %d rows, want %d (member row already covers the park)", got-before, 0)
	}
}

func TestNotification_QuestionDedupe_NoMemberRowStillNotifies(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	addTestSubscriber(t, issueID, "member", testUserID, "creator")

	publishAgentQuestion(bus, issueID, agentQuestionInput())

	items := inboxItemsForRecipient(t, queries, testUserID)
	if len(items) != 1 {
		t.Fatalf("expected 1 inbox item for the park with no member row, got %d", len(items))
	}
	if body := util.TextToPtr(items[0].Body); body == nil || *body != "Which checkout should this land in?" {
		t.Fatalf("body = %v, want the first question's prompt", body)
	}
}
