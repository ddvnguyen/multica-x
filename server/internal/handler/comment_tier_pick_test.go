package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
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
// `@agent` and `@agent#tier-fast` in one comment are distinct triggers. With
// tier-fast's slot occupied, the auto resolver falls to tier-slow while the
// explicit pick parks on tier-fast — two runs, neither falling back onto the
// other's tier.
func TestCommentTierPick_PlainPlusTieredRunsBoth(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, tierSlow := tierAgentFixture(t)
	issueID := createCommentTriggerPreviewIssue(t, "tier pick mixed", "", "")

	// Occupy tier-fast's single slot so auto resolution falls through.
	agentUUID := util.MustParseUUID(agentID)
	fastRuntime := ""
	if err := testPool.QueryRow(context.Background(),
		`SELECT runtime_id::text FROM model_tier WHERE id = $1`, tierFast).Scan(&fastRuntime); err != nil {
		t.Fatalf("tier runtime: %v", err)
	}
	var blockerID string
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`,
		agentUUID, util.MustParseUUID(fastRuntime)).Scan(&blockerID); err != nil {
		t.Fatalf("insert blocker: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, blockerID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	content := fmt.Sprintf("[@Agent](mention://agent/%s) plus [@Agent#tier-fast](mention://agent/%s) please", agentID, agentID)
	var posted CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)).Want(http.StatusCreated).JSON(&posted)
	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 2 {
		t.Fatalf("queued tasks = %d, want 2 (auto on slow + parked explicit on fast)", got)
	}
	rows, err := testPool.Query(context.Background(),
		`SELECT tier_id::text, tier_explicit::text, tier_parked::text FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, agentUUID)
	if err != nil {
		t.Fatalf("read stamps: %v", err)
	}
	defer rows.Close()
	type stamp struct{ tier, explicit, parked string }
	var stamps []stamp
	for rows.Next() {
		var s stamp
		if err := rows.Scan(&s.tier, &s.explicit, &s.parked); err != nil {
			t.Fatalf("scan stamp: %v", err)
		}
		stamps = append(stamps, s)
	}
	if len(stamps) != 2 {
		t.Fatalf("stamps = %+v, want 2", stamps)
	}
	byTier := map[string]stamp{}
	for _, s := range stamps {
		byTier[s.tier] = s
	}
	auto, ok := byTier[tierSlow]
	if !ok || auto.explicit != "false" || auto.parked != "false" {
		t.Errorf("auto stamp = %+v, want tier-slow/non-explicit/unparked", auto)
	}
	explicit, ok := byTier[tierFast]
	if !ok || explicit.explicit != "true" || explicit.parked != "true" {
		t.Errorf("explicit stamp = %+v, want tier-fast/explicit/parked", explicit)
	}
}
