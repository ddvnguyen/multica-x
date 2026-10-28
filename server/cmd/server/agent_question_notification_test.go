package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// publishAgentQuestion fires the event the daemon's message ingest publishes
// for a parked ask, so the test exercises the real listener chain.
//
// The task id is deliberately not a UUID: a run's row can be gone by the time
// the question lands, and that has to degrade to "no actor" rather than drop
// the ask or panic the bus dispatch.
func publishAgentQuestion(bus *events.Bus, issueID string, input map[string]any) {
	bus.Publish(events.Event{
		Type:        protocol.EventTaskMessage,
		WorkspaceID: testWorkspaceID,
		TaskID:      "test-task",
		Payload: protocol.TaskMessagePayload{
			TaskID:  "test-task",
			IssueID: issueID,
			Seq:     7,
			Type:    "question",
			Content: "❓ The agent needs your answer",
			Input:   input,
		},
	})
}

func agentQuestionInput() map[string]any {
	return map[string]any{
		"questions": []any{
			map[string]any{
				"id":       "q1",
				"header":   "Scope",
				"question": "Which checkout should this land in?",
				"options": []any{
					map[string]any{"label": "Web", "description": "The Next.js app"},
					map[string]any{"label": "Mobile"},
				},
			},
		},
	}
}

// TestNotification_AgentQuestion_ParksInInbox is the whole point of the ask
// surface: a run that parks on a question has to reach the member somewhere
// they are actually looking. The transcript row alone is invisible until the
// issue is open, so the listener turns it into an action-required inbox row
// carrying the question's first prompt.
func TestNotification_AgentQuestion_ParksInInbox(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	// Production subscribes the creator through the ordinary subscriber
	// rules; the row is what this test is about, so subscribe directly.
	addTestSubscriber(t, issueID, "member", testUserID, "creator")

	publishAgentQuestion(bus, issueID, agentQuestionInput())

	items := inboxItemsForRecipient(t, queries, testUserID)
	if len(items) != 1 {
		t.Fatalf("expected 1 inbox item for the parked question, got %d", len(items))
	}
	item := items[0]
	if item.Type != "agent_question" {
		t.Fatalf("type = %q, want agent_question", item.Type)
	}
	if item.Severity != "action_required" {
		t.Fatalf("severity = %q, want action_required", item.Severity)
	}
	if !item.IssueID.Valid || util.UUIDToString(item.IssueID) != issueID {
		t.Fatalf("issue_id = %v, want %s", item.IssueID, issueID)
	}
	if body := util.TextToPtr(item.Body); body == nil || *body != "Which checkout should this land in?" {
		t.Fatalf("body = %v, want the first question's prompt", body)
	}
}

// TestNotification_AgentQuestion_IgnoresOtherMessages: every streamed message
// goes through this listener, so anything that is not a question must leave
// the inbox untouched — a run's ordinary text is progress, not an ask.
func TestNotification_AgentQuestion_IgnoresOtherMessages(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)

	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})
	addTestSubscriber(t, issueID, "member", testUserID, "creator")

	bus.Publish(events.Event{
		Type:        protocol.EventTaskMessage,
		WorkspaceID: testWorkspaceID,
		Payload: protocol.TaskMessagePayload{
			TaskID:  "test-task",
			IssueID: issueID,
			Seq:     8,
			Type:    "text",
			Content: "still working",
		},
	})

	if items := inboxItemsForRecipient(t, queries, testUserID); len(items) != 0 {
		t.Fatalf("expected no inbox items for a text message, got %d", len(items))
	}
}

// TestFirstQuestionPrompt covers the row's one line: the first question with a
// prompt wins, and a payload with none yields an empty body rather than a
// panic or a stray rendering of the map.
func TestFirstQuestionPrompt(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"first prompt", agentQuestionInput(), "Which checkout should this land in?"},
		{"skips an empty sibling", map[string]any{"questions": []any{
			map[string]any{"id": "empty"},
			map[string]any{"question": "  Which branch?  "},
		}}, "Which branch?"},
		{"no questions", map[string]any{}, ""},
		{"wrong shape", map[string]any{"questions": "nope"}, ""},
		{"nil input", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstQuestionPrompt(tc.input); got != tc.want {
				t.Fatalf("firstQuestionPrompt() = %q, want %q", got, tc.want)
			}
		})
	}
}
