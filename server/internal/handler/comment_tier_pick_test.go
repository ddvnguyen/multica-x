package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestCommentTierPick_PreviewUnknownTierBlocked pins the M-359 preview:
// `[@Agent#nope](mention://agent/<id>)` surfaces tier_unavailable in the
// blocked list instead of resolving to a plain trigger.
func TestCommentTierPick_PreviewUnknownTierBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, _ := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick unknown", "", "")

	content := fmt.Sprintf("[@Agent#nope](mention://agent/%s) please take a look", agentID)
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if len(preview.Agents) != 0 {
		t.Fatalf("preview agents = %d, want 0 (unknown tier blocks)", len(preview.Agents))
	}
	found := false
	for _, b := range preview.Blocked {
		if b.TargetID == agentID && b.ReasonCode == ReasonTierUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("preview blocked = %+v, want tier_unavailable for %s", preview.Blocked, agentID)
	}
	_ = tierFast
}

// TestCommentTierPick_PreviewKnownTierTriggers pins the positive preview:
// a suffix matching an enabled selected tier still resolves to a trigger.
func TestCommentTierPick_PreviewKnownTierTriggers(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, _, _ := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick known", "", "")

	content := fmt.Sprintf("[@Agent#tier-fast](mention://agent/%s) please take a look", agentID)
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if len(preview.Agents) != 1 {
		t.Fatalf("preview agents = %d, want 1", len(preview.Agents))
	}
	if len(preview.Blocked) != 0 {
		t.Fatalf("preview blocked = %+v, want empty", preview.Blocked)
	}
}

// TestCommentTierPick_PostUnknownTierBlocked pins the submit path: posting a
// comment with an unknown tier suffix blocks with tier_unavailable and
// enqueues nothing.
func TestCommentTierPick_PostUnknownTierBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, _, _ := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick post blocked", "", "")

	content := fmt.Sprintf("[@Agent#nope](mention://agent/%s) please take a look", agentID)
	var posted CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)).Want(http.StatusCreated).JSON(&posted)
	outcome := findCommentOutcome(t, posted.TriggerOutcomes, agentID)
	if outcome.Status != DispatchBlocked || outcome.ReasonCode != ReasonTierUnavailable {
		t.Fatalf("outcome = %+v, want blocked/tier_unavailable", outcome)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 0 {
		t.Fatalf("queued tasks = %d, want 0", got)
	}
}

// TestCommentTierPick_PostKnownTierQueues pins the submit success path: the
// known-tier mention enqueues one task stamped with that tier, explicit.
func TestCommentTierPick_PostKnownTierQueues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, _ := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick post queued", "", "")

	content := fmt.Sprintf("[@Agent#tier-fast](mention://agent/%s) please take a look", agentID)
	var posted CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)).Want(http.StatusCreated).JSON(&posted)
	outcome := findCommentOutcome(t, posted.TriggerOutcomes, agentID)
	if outcome.Status != DispatchQueued {
		t.Fatalf("outcome = %+v, want queued", outcome)
	}
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks = %d, want 1", got)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	var gotTier, gotExplicit string
	if err := testPool.QueryRow(context.Background(),
		`SELECT tier_id::text, tier_explicit::text FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`,
		issueID, agentID).Scan(&gotTier, &gotExplicit); err != nil {
		t.Fatalf("read task stamp: %v", err)
	}
	if gotTier != tierFast {
		t.Errorf("task tier = %s, want %s", gotTier, tierFast)
	}
	if gotExplicit != "true" {
		t.Errorf("task tier_explicit = %s, want true", gotExplicit)
	}
}
