package main

// M-333: the task:cancelled listener archives the ask/plan inbox rows whose
// card/plan the cancel settle just closed. The timeout-escalation rows (details
// carry only the parked task id) are deliberately NOT archived here — they are
// created after the settle that withdraws the card, and they stay as the lead's
// pointer to the expired ask until it is answered. Rows without ask/plan
// details (the pre-ask task:message questions) match nothing by construction.

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// insertResolvedAskScenario seeds one issue with three agent_question rows on
// the same evidence task: an evidence row (card id in details), a
// timeout-escalation row (task id in details), and a legacy row (empty
// details). Returns the issue id.
func insertResolvedAskScenario(t *testing.T, queries *db.Queries, bus *events.Bus) string {
	t.Helper()
	issueID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		cleanupInboxForIssue(t, issueID)
		cleanupTestIssue(t, issueID)
	})

	cardID := uuid.NewString()
	taskID := uuid.NewString()
	agentID := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_question_card (id, workspace_id, issue_id, task_id, agent_id, status, questions)
		VALUES ($1, $2, $3, $4, $5, 'withdrawn', '[]'::jsonb)
	`, cardID, testWorkspaceID, issueID, taskID, agentID); err != nil {
		t.Fatalf("insert withdrawn card: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_question_card WHERE id = $1`, cardID)
	})

	rows := []struct {
		name    string
		details string
		want    string // 'archived' or 'active' after one task:cancelled event
	}{
		{"evidence", fmt.Sprintf(`{"card_id":%q}`, cardID), "archived"},
		{"escalation", fmt.Sprintf(`{"task_id":%q}`, taskID), "active"},
		{"legacy", "{}", "active"},
	}
	for _, row := range rows {
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO inbox_item (id, workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, details)
			VALUES ($1, $2, 'member', $3, 'agent_question', 'action_required', $4, $5, $6::jsonb)
		`, uuid.NewString(), testWorkspaceID, testUserID, issueID, row.name, row.details); err != nil {
			t.Fatalf("insert %s row: %v", row.name, err)
		}
	}

	bus.Publish(events.Event{
		Type:        protocol.EventTaskCancelled,
		WorkspaceID: testWorkspaceID,
		Payload: map[string]any{
			"task_id":  taskID,
			"issue_id": issueID,
			"status":   "cancelled",
		},
	})

	for _, row := range rows {
		var archived bool
		if err := testPool.QueryRow(context.Background(),
			`SELECT archived FROM inbox_item WHERE issue_id = $1 AND title = $2`,
			issueID, row.name).Scan(&archived); err != nil {
			t.Fatalf("load %s row: %v", row.name, err)
		}
		wantArchived := row.want == "archived"
		if archived != wantArchived {
			t.Fatalf("%s row archived = %v, want %v", row.name, archived, wantArchived)
		}
	}

	// The withdrawal was already committed before the event; nothing left to
	// prove about the card, but the row count sanity-checks the scenario.
	items := inboxItemsForRecipient(t, queries, testUserID)
	count := 0
	for _, item := range items {
		if item.IssueID.Valid && util.UUIDToString(item.IssueID) == issueID {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("active rows after the event = %d, want 2 (escalation + legacy)", count)
	}
	return issueID
}

func TestNotification_TaskCancelled_ArchivesResolvedAskRows(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	insertResolvedAskScenario(t, queries, bus)
}

// The same archive also runs for failed runs: terminateTasksInTx settles the
// parked ask/plan inside the fail transaction, and task:failed fires after it.
func TestNotification_TaskFailed_ArchivesResolvedAskRows(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	insertResolvedAskScenario(t, queries, bus)
}
