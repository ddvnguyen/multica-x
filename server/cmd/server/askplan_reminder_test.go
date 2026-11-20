package main

// M-362: the mid-timeout reminder — one bump at ~50% of the effective timeout,
// never after resolution, never twice. Drives sweepAskPlanRound against the
// shared DB like the escalation suite: the round function is the whole
// behavior, the 30s ticker around it already covered.
//
//   - a park aged past half its timeout reminds once: the evidence row is
//     stamped, one extra inbox row per member lands with `reminder: true`,
//     and the card stays open / the task stays blocked;
//   - a second round reminds nothing: the stamp is the idempotency gate;
//   - an answered park never reminds: the candidate join only sees live
//     evidence;
//   - the reminder row resolves with the original: answering the card
//     archives both the park row and the bump through the same
//     ArchiveResolvedAskPlan path.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func askPlanReminderCount(t *testing.T, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND archived = false
		  AND details->>'reminder' = 'true'
	`, issueID).Scan(&n); err != nil {
		t.Fatalf("count reminder rows: %v", err)
	}
	return n
}

func TestAskPlanReminder_HalfTimeoutRemindsOnce(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  170 * time.Second, // default 300s: past half, before expiry
		leadType: "agent",
	})
	queries := db.New(testPool)
	before := askPlanInboxCount(t, fx.issueID, testUserID)

	expired, reminded, err := sweepAskPlanParks(context.Background(), queries, askPlanTaskService())
	if err != nil {
		t.Fatalf("sweepAskPlanParks: %v", err)
	}
	if expired != 0 {
		t.Fatalf("expired = %d, want 0 (park is before its deadline)", expired)
	}
	if reminded != 1 {
		t.Fatalf("reminded = %d, want 1", reminded)
	}
	if got := askPlanInboxCount(t, fx.issueID, testUserID) - before; got != 1 {
		t.Fatalf("new inbox rows = %d, want 1 reminder bump", got)
	}
	if got := askPlanReminderCount(t, fx.issueID); got != 1 {
		t.Fatalf("reminder rows = %d, want 1", got)
	}
	// Still a live park: nothing about the wait changed.
	if status, _ := askPlanTaskStatus(t, fx.taskID); status != "blocked" {
		t.Fatalf("task status = %q, want blocked after reminder", status)
	}

	// Second round: the stamp holds, nothing re-fires.
	if _, reminded, err := sweepAskPlanParks(context.Background(), queries, askPlanTaskService()); err != nil {
		t.Fatalf("second sweepAskPlanParks: %v", err)
	} else if reminded != 0 {
		t.Fatalf("second round reminded = %d, want 0", reminded)
	}
	if got := askPlanReminderCount(t, fx.issueID); got != 1 {
		t.Fatalf("reminder rows after second round = %d, want 1", got)
	}
}

func TestAskPlanReminder_FreshParkDoesNotRemind(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  10 * time.Second,
		leadType: "agent",
	})
	if _, reminded, err := sweepAskPlanParks(context.Background(), db.New(testPool), askPlanTaskService()); err != nil {
		t.Fatalf("sweepAskPlanParks: %v", err)
	} else if reminded != 0 {
		t.Fatalf("reminded = %d, want 0 (park is before half its timeout)", reminded)
	}
	if got := askPlanReminderCount(t, fx.issueID); got != 0 {
		t.Fatalf("reminder rows = %d, want 0", got)
	}
}

func TestAskPlanReminder_AnsweredParkNeverReminds(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		answered: true,
		leadType: "agent",
	})
	if _, reminded, err := sweepAskPlanParks(context.Background(), db.New(testPool), askPlanTaskService()); err != nil {
		t.Fatalf("sweepAskPlanParks: %v", err)
	} else if reminded != 0 {
		t.Fatalf("reminded = %d, want 0 (answered parks are not candidates)", reminded)
	}
	if got := askPlanReminderCount(t, fx.issueID); got != 0 {
		t.Fatalf("reminder rows = %d, want 0", got)
	}
}

func TestAskPlanReminder_ReminderRowArchivedOnAnswer(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  170 * time.Second,
		leadType: "agent",
	})
	queries := db.New(testPool)
	if _, reminded, err := sweepAskPlanParks(context.Background(), queries, askPlanTaskService()); err != nil {
		t.Fatalf("sweepAskPlanParks: %v", err)
	} else if reminded != 1 {
		t.Fatalf("reminded = %d, want 1", reminded)
	}
	// The Owner answers: the service archives both the park row and the bump
	// through the same ArchiveResolvedAskPlan path the answer handler owns.
	if _, _, err := askPlanTaskService().AnswerQuestionCard(
		context.Background(),
		parseUUID(fx.cardID),
		parseUUID(testWorkspaceID),
		parseUUID(testUserID),
		service.AnsweredByMember,
		json.RawMessage(`[{"selected":[0],"text":""}]`),
	); err != nil {
		t.Fatalf("AnswerQuestionCard: %v", err)
	}
	if got := askPlanReminderCount(t, fx.issueID); got != 0 {
		t.Fatalf("active reminder rows after answer = %d, want 0", got)
	}
	var active int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND archived = false
	`, fx.issueID).Scan(&active); err != nil {
		t.Fatalf("count active rows: %v", err)
	}
	if active != 0 {
		t.Fatalf("active rows after answer = %d, want 0 (park row and bump both archived)", active)
	}
}

func TestIsNotifMuted_PlanReviewFollowsAgentActivity(t *testing.T) {
	if got := isNotifMuted(map[string]string{"agent_activity": "muted"}, "plan_review"); !got {
		t.Fatal("muted agent_activity must silence plan_review")
	}
	if got := isNotifMuted(map[string]string{}, "plan_review"); got {
		t.Fatal("default prefs must deliver plan_review")
	}
}
