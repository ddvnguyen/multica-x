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

// TestCommentTierPick_MultiTierCommentRunsBoth pins the M-359 no-collapse
// invariant: `@agent#tier-fast` + `@agent#tier-slow` in one comment enqueue
// TWO sibling tasks, each stamped with its own tier — never one run with the
// first tier while the second pick is silently dropped.
func TestCommentTierPick_MultiTierCommentRunsBoth(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, tierSlow := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick multi", "", "")

	content := fmt.Sprintf("[@Agent#tier-fast](mention://agent/%s) and [@Agent#tier-slow](mention://agent/%s) handle both", agentID, agentID)
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if len(preview.Agents) != 2 {
		t.Fatalf("preview agents = %d, want 2 (one per explicit tier)", len(preview.Agents))
	}

	var posted CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)).Want(http.StatusCreated).JSON(&posted)
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 2 {
		t.Fatalf("queued tasks = %d, want 2 sibling tier tasks", got)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	rows, err := testPool.Query(context.Background(),
		`SELECT tier_id::text, tier_explicit::text FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`,
		issueID, agentID)
	if err != nil {
		t.Fatalf("read task stamps: %v", err)
	}
	defer rows.Close()
	stamps := map[string]string{}
	for rows.Next() {
		var tier, explicit string
		if err := rows.Scan(&tier, &explicit); err != nil {
			t.Fatalf("scan stamp: %v", err)
		}
		stamps[tier] = explicit
	}
	if len(stamps) != 2 || stamps[tierFast] != "true" || stamps[tierSlow] != "true" {
		t.Fatalf("task stamps = %+v, want both tiers explicit", stamps)
	}
}

// TestCommentTierPick_PlainPlusTieredRunsBoth pins the mixed case: plain
// `@agent` and `@agent#tier-fast` in one comment are distinct triggers —
// one auto-resolved run plus one explicit run.
func TestCommentTierPick_PlainPlusTieredRunsBoth(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, _ := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick mixed", "", "")

	content := fmt.Sprintf("[@Agent](mention://agent/%s) plus [@Agent#tier-fast](mention://agent/%s) please", agentID, agentID)
	var posted CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)).Want(http.StatusCreated).JSON(&posted)
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 2 {
		t.Fatalf("queued tasks = %d, want 2 (auto + explicit)", got)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	var autoCount, explicitCount int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE tier_id IS NULL), count(*) FILTER (WHERE tier_id::text = $1 AND tier_explicit) FROM agent_task_queue WHERE issue_id = $2 AND agent_id = $3`,
		tierFast, issueID, agentID).Scan(&autoCount, &explicitCount); err != nil {
		t.Fatalf("count stamps: %v", err)
	}
	if autoCount != 1 || explicitCount != 1 {
		t.Fatalf("auto = %d, explicit = %d, want 1 + 1", autoCount, explicitCount)
	}
}
