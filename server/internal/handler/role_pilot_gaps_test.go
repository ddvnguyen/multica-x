package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// makeRoleInvocableForPilot turns one setupRoleRun Role into the production
// pilot shape: use_tiers on (M-579) and invocable by the test member, so the
// admission gates under test are the only reason a trigger changes verdict.
func makeRoleInvocableForPilot(t *testing.T, roleID string) {
	t.Helper()
	dbfx.Exec(t, `UPDATE agent SET use_tiers = true, permission_mode = 'public_to' WHERE id = $1`, roleID)
	dbfx.Exec(t, `INSERT INTO agent_invocation_target (agent_id, target_type, target_id, created_by)
		VALUES ($1, 'workspace', $2, $3)`, roleID, testWorkspaceID, testUserID)
}

func tierNameByID(t *testing.T, tierID string) string {
	t.Helper()
	var name string
	if err := testPool.QueryRow(context.Background(),
		`SELECT name FROM model_tier WHERE id = $1`, tierID).Scan(&name); err != nil {
		t.Fatalf("read tier name: %v", err)
	}
	return name
}

// F2 (M-589): `@Role#tier` on a use_tiers Role must queue the run. The mention
// path validated the tier name against agent_tier, which a Role never
// populates, so a valid role-keyed tier arrived as tier_unavailable.
func TestRoleMentionExplicitTierQueues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupRoleRun(t, 99, false)
	makeRoleInvocableForPilot(t, fix.roleID)
	issueID := createCommentTriggerPreviewIssue(t, "m589 role tier pick "+roleTestName(""), "", "")

	content := fmt.Sprintf("[@Role#%s](mention://agent/%s) please take a look", tierNameByID(t, fix.tierA), fix.roleID)
	preview := previewCommentTriggersForTest(t, issueID, map[string]any{"content": content})
	if len(preview.Agents) != 1 || len(preview.Blocked) != 0 {
		t.Fatalf("preview agents=%d blocked=%+v, want the Role tier pick to resolve", len(preview.Agents), preview.Blocked)
	}

	posted := postCommentForTriggerPreviewTest(t, issueID, map[string]any{"content": content})
	var stamped string
	if err := testPool.QueryRow(context.Background(),
		`SELECT tier_id::text FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`,
		issueID, fix.roleID).Scan(&stamped); err != nil {
		t.Fatalf("Role tier pick did not queue a task (comment %s): %v", posted, err)
	}
	if stamped != fix.tierA {
		t.Fatalf("queued tier = %s, want %s", stamped, fix.tierA)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
}

// F3 (M-589): quick-create on a runtime-less Role. The Role passes readiness
// (its route comes from the tier) but the daemon-CLI gates read
// agent.RuntimeID, so the request was refused as "runtime no longer
// registered" before the tier could stamp one.
func TestQuickCreateIssueRoleWithoutRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupRoleRun(t, 1, false)
	makeRoleInvocableForPilot(t, fix.roleID)

	testutil.Call(t, testHandler.QuickCreateIssue,
		newRequest(http.MethodPost, "/api/issues/quick-create", map[string]any{
			"agent_id": fix.roleID,
			"prompt":   "Draft the follow-up issue",
		})).Want(http.StatusAccepted)

	var tierID, runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(tier_id::text, ''), COALESCE(runtime_id::text, '')
		FROM agent_task_queue
		WHERE agent_id = $1 AND context->>'type' = 'quick_create' AND status = 'queued'
	`, fix.roleID).Scan(&tierID, &runtimeID); err != nil {
		t.Fatalf("Role quick-create did not queue a task: %v", err)
	}
	if tierID != fix.tierA || runtimeID != fix.rtA {
		t.Fatalf("quick-create stamp tier=%s runtime=%s, want %s/%s", tierID, runtimeID, fix.tierA, fix.rtA)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1 AND context->>'type' = 'quick_create'`, fix.roleID)
	})
}
