package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// roleRunFixture is one Role (model-free, flag on) with two role-keyed tiers.
// Tier A is first in the Role's ordered list; B is the higher-idx fallback.
type roleRunFixture struct {
	roleID, tierA, tierB, rtA, rtB string
	issueID                        string
}

// insertReturning inserts a fixture row and returns its UUID as text.
func insertReturning(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	dbfx.QueryRow(t, sql, args...).Scan(&id)
	return id
}

// setupRoleRun creates a Role and its role-keyed tier list in the shared test
// workspace. slotsA controls tier A's provider slots; when onlyA is true only
// tier A is in the Role's list (so a full tier A parks instead of falling back).
func setupRoleRun(t *testing.T, slotsA int32, onlyA bool) roleRunFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	roleKey := "engineer"

	roleID := dbfx.Agent(t, roleTestName("m510-role"), "", testutil.Cols{
		"subtype":         "role",
		"tier_role":       roleKey,
		"use_tiers":       false,
		"permission_mode": "private",
	})
	rtA := dbfx.Runtime(t, "m510-rt-a-"+suffix)
	rtB := dbfx.Runtime(t, "m510-rt-b-"+suffix)
	tierA := insertReturning(t, fmt.Sprintf(`
		INSERT INTO model_tier (workspace_id, name, idx, runtime_id, provider, model, provider_slots, enabled, roles)
		VALUES ($1, 'm510-tier-a-%s', 0, $2, 'codebuddy', 'm510-model-a', %d, true, ARRAY['%s']::text[])
		RETURNING id`, suffix, slotsA, roleKey), testWorkspaceID, rtA)
	var tierB string
	roleRows := fmt.Sprintf(`($1, $2, '%s', 0)`, roleKey)
	if !onlyA {
		tierB = insertReturning(t, fmt.Sprintf(`
			INSERT INTO model_tier (workspace_id, name, idx, runtime_id, provider, model, provider_slots, enabled, roles)
			VALUES ($1, 'm510-tier-b-%s', 1, $2, 'codebuddy', 'm510-model-b', 99, true, ARRAY['%s']::text[])
			RETURNING id`, suffix, roleKey), testWorkspaceID, rtB)
		roleRows += fmt.Sprintf(`, ($1, $3, '%s', 1)`, roleKey)
	}
	roleArgs := []any{testWorkspaceID, tierA}
	if !onlyA {
		roleArgs = append(roleArgs, tierB)
	}
	dbfx.Exec(t, `INSERT INTO model_tier_role (workspace_id, tier_id, role, idx) VALUES `+roleRows, roleArgs...)

	issueID := dbfx.Issue(t, "M-510 role run "+suffix, testutil.Cols{
		"assignee_type": "agent", "assignee_id": roleID,
	})
	return roleRunFixture{roleID: roleID, tierA: tierA, tierB: tierB, rtA: rtA, rtB: rtB, issueID: issueID}
}

func (f roleRunFixture) issue(t *testing.T) db.Issue {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(f.issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

func TestRoleResolverAutoStampAndIdentity(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()

	task, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("enqueue Role auto pick: %v", err)
	}
	if uuidToString(task.AgentID) != fix.roleID {
		t.Fatalf("task identity agent=%s, want Role %s", uuidToString(task.AgentID), fix.roleID)
	}
	if uuidToString(task.TierID) != fix.tierA || uuidToString(task.RuntimeID) != fix.rtA {
		t.Fatalf("auto stamp tier=%s runtime=%s, want %s/%s", uuidToString(task.TierID), uuidToString(task.RuntimeID), fix.tierA, fix.rtA)
	}
	if !task.ModelOverride.Valid || task.ModelOverride.String != "m510-model-a" {
		t.Fatalf("auto stamp model=%+v", task.ModelOverride)
	}
	// Role stays model-free: the tier supplies model/runtime only on the task.
	var runtimeNull bool
	var modelText string
	var useTiers bool
	dbfx.QueryRow(t, `SELECT runtime_id IS NULL, COALESCE(model, ''), use_tiers FROM agent WHERE id = $1`, fix.roleID).
		Scan(&runtimeNull, &modelText, &useTiers)
	if !runtimeNull || modelText != "" || useTiers {
		t.Fatalf("Role row changed: runtime_null=%v model=%q use_tiers=%v", runtimeNull, modelText, useTiers)
	}
}

func TestRoleResolverForcedStamp(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()

	task, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, fix.issue(t), "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(fix.tierB), Requester: parseUUID(testUserID)})
	if err != nil {
		t.Fatalf("enqueue Role forced pick: %v", err)
	}
	if uuidToString(task.TierID) != fix.tierB || uuidToString(task.RuntimeID) != fix.rtB {
		t.Fatalf("forced stamp tier=%s runtime=%s, want %s/%s", uuidToString(task.TierID), uuidToString(task.RuntimeID), fix.tierB, fix.rtB)
	}
	if !task.TierExplicit {
		t.Fatal("forced pick must mark tier_explicit")
	}
	if !task.ModelOverride.Valid || task.ModelOverride.String != "m510-model-b" {
		t.Fatalf("forced stamp model=%+v", task.ModelOverride)
	}
}

func TestRoleResolverInvalidAndDisabledTier(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	// A tier in a different role is not selectable by this Role.
	foreign := insertReturning(t, fmt.Sprintf(`
		INSERT INTO model_tier (workspace_id, name, idx, runtime_id, provider, model, provider_slots, enabled, roles)
		VALUES ($1, 'm510-foreign-%s', 5, $2, 'codebuddy', 'm510-model-foreign', 99, true, ARRAY['management']::text[])
		RETURNING id`, suffix), testWorkspaceID, fix.rtB)
	if _, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, fix.issue(t), "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(foreign), Requester: parseUUID(testUserID)}); err == nil {
		t.Fatal("tier outside the Role's list was accepted")
	}
	// A disabled tier in the Role's list is also blocked.
	dbfx.Exec(t, `UPDATE model_tier SET enabled = false WHERE id = $1`, fix.tierB)
	if _, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, fix.issue(t), "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(fix.tierB), Requester: parseUUID(testUserID)}); err == nil {
		t.Fatal("disabled tier in the Role's list was accepted")
	}
}

func TestRoleResolverAllBusyParksAndRetries(t *testing.T) {
	fix := setupRoleRun(t, 1, true)
	ctx := context.Background()
	// Occupy tier A's single slot with a running task.
	dbfx.Task(t, fix.roleID, testutil.Cols{
		"runtime_id": fix.rtA, "tier_id": fix.tierA, "status": "running",
	})
	task, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("enqueue Role with full tier: %v", err)
	}
	if task.RuntimeID.Valid || !task.TierParked {
		t.Fatalf("full tier must park a runtimeless row: runtime=%+v parked=%v", task.RuntimeID, task.TierParked)
	}
	// Free the slot, then the capacity-change sweep must stamp this exact row.
	dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE runtime_id = $1 AND status = 'running' AND tier_id = $2`, fix.rtA, fix.tierA)
	stamped := testHandler.TaskService.RestampRuntimelessTierTasks(ctx)
	found := false
	for _, row := range stamped {
		if uuidToString(row.ID) == uuidToString(task.ID) {
			found = true
			if uuidToString(row.RuntimeID) != fix.rtA || uuidToString(row.TierID) != fix.tierA {
				t.Fatalf("restamped row tier=%s runtime=%s", uuidToString(row.TierID), uuidToString(row.RuntimeID))
			}
		}
	}
	if !found {
		t.Fatal("parked Role row was not restamped after a slot freed")
	}
}

func TestRoleResolverRetryRevalidates(t *testing.T) {
	ctx := context.Background()
	// (a) A forced pick parked on a tier that later leaves the Role's list must
	// stay parked — the forced tier is revalidated on every retry.
	forced := setupRoleRun(t, 1, true)
	dbfx.Task(t, forced.roleID, testutil.Cols{
		"runtime_id": forced.rtA, "tier_id": forced.tierA, "status": "running",
	})
	parked, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, forced.issue(t), "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(forced.tierA), Requester: parseUUID(testUserID)})
	if err != nil {
		t.Fatalf("park forced Role pick: %v", err)
	}
	dbfx.Exec(t, `DELETE FROM model_tier_role WHERE tier_id = $1 AND role = 'engineer'`, forced.tierA)
	for _, row := range testHandler.TaskService.RestampRuntimelessTierTasks(ctx) {
		if uuidToString(row.ID) == uuidToString(parked.ID) {
			t.Fatal("forced pick was restamped after its tier left the Role list")
		}
	}

	// (b) An archived Role keeps its parked row queued on retry (fail closed).
	archived := setupRoleRun(t, 1, true)
	dbfx.Task(t, archived.roleID, testutil.Cols{
		"runtime_id": archived.rtA, "tier_id": archived.tierA, "status": "running",
	})
	parked2, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, archived.issue(t), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("park auto Role pick: %v", err)
	}
	dbfx.Exec(t, `UPDATE agent SET archived_at = now() WHERE id = $1`, archived.roleID)
	for _, row := range testHandler.TaskService.RestampRuntimelessTierTasks(ctx) {
		if uuidToString(row.ID) == uuidToString(parked2.ID) {
			t.Fatal("archived Role's parked row was restamped")
		}
	}
}

func TestRoleResolverMemberSlotRace(t *testing.T) {
	fix := setupRoleRun(t, 99, false)
	ctx := context.Background()
	// Replace the legacy tier-level route with one 1-slot member so two forced
	// picks to the same tier cannot both resolve a slot.
	dbfx.Exec(t, `DELETE FROM model_tier_member WHERE tier_id = $1`, fix.tierB)
	dbfx.Exec(t, `INSERT INTO model_tier_member (tier_id, position, provider, model, runtime_id, provider_slots)
		VALUES ($1, 0, 'codebuddy', 'm510-member-model', $2, 1)`, fix.tierB, fix.rtB)

	first, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, fix.issue(t), "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(fix.tierB), Requester: parseUUID(testUserID)})
	if err != nil {
		t.Fatalf("first forced pick: %v", err)
	}
	if !first.TierMemberID.Valid {
		t.Fatal("first forced pick did not stamp the tier member")
	}
	if first.ModelOverride.String != "m510-member-model" {
		t.Fatalf("member model not stamped: %q", first.ModelOverride.String)
	}
	// Capacity counts dispatched/running rows; make the first pick occupy its slot.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running' WHERE id = $1`, uuidToString(first.ID))
	// A separate issue assigned to the same Role races for the single slot.
	secondIssue := dbfx.Issue(t, "M-510 member race "+roleTestName(""), testutil.Cols{
		"assignee_type": "agent", "assignee_id": fix.roleID,
	})
	issue2, err := testHandler.Queries.GetIssue(ctx, parseUUID(secondIssue))
	if err != nil {
		t.Fatalf("load second issue: %v", err)
	}
	second, err := testHandler.TaskService.EnqueueTaskForIssueWithTierHandoff(ctx, issue2, "", parseUUID(testUserID),
		service.TierTriggerOverride{TierID: parseUUID(fix.tierB), Requester: parseUUID(testUserID)})
	if err != nil {
		t.Fatalf("second forced pick: %v", err)
	}
	if !second.TierParked || second.RuntimeID.Valid {
		t.Fatalf("second pick on a full member must park: parked=%v runtime=%+v", second.TierParked, second.RuntimeID)
	}
}

func TestRoleResolverPrivilegeAndWorkspaceDenial(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()
	// A member of another workspace cannot pick this Role.
	otherUser := dbfx.User(t, "m510 outsider", fmt.Sprintf("m510-outsider-%d@multica.test", time.Now().UnixNano()))
	otherWS := dbfx.Workspace(t, "m510 outsider ws", "m510-outsider-"+fmt.Sprint(time.Now().UnixNano()))
	dbfx.Member(t, otherWS, otherUser, "owner")
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(otherUser)); err == nil {
		t.Fatal("cross-workspace pick was authorized")
	}
	// A regular member without a public_to target cannot pick a private Role.
	memberUser := dbfx.User(t, "m510 member", fmt.Sprintf("m510-member-%d@multica.test", time.Now().UnixNano()))
	dbfx.Member(t, testWorkspaceID, memberUser, "member")
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(memberUser)); err == nil {
		t.Fatal("unauthorized member pick was authorized")
	}
}

func TestRoleResolverClaimDeliversAsRole(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()
	task, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("enqueue Role: %v", err)
	}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(fix.rtA))
	if err != nil {
		t.Fatalf("claim Role task: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != uuidToString(task.ID) {
		t.Fatalf("claimed %+v, want %s", claimed, uuidToString(task.ID))
	}
	rt, err := testHandler.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID: parseUUID(fix.rtA), WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+fix.rtA+"/tasks/claim", nil)
	resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, claimed, rt, fix.rtA, testWorkspaceID)
	if failure != nil {
		t.Fatalf("deliver Role task: %+v", failure)
	}
	// The run identity is the Role; the model came from the tier stamp.
	if resp.AgentID != fix.roleID {
		t.Fatalf("claim response agent=%s, want Role %s", resp.AgentID, fix.roleID)
	}
}

func TestRoleResolverArchivedOrDeletedRoleFailsClosed(t *testing.T) {
	fix := setupRoleRun(t, 1, false)
	ctx := context.Background()
	dbfx.Exec(t, `UPDATE agent SET archived_at = now() WHERE id = $1`, fix.roleID)
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID)); err == nil {
		t.Fatal("archived Role was accepted at pick time")
	}
	dbfx.Exec(t, `DELETE FROM agent WHERE id = $1`, fix.roleID)
	if _, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID)); err == nil {
		t.Fatal("deleted Role was accepted at pick time")
	}
}

func TestRoleResolverStampedWorkUnchanged(t *testing.T) {
	fix := setupRoleRun(t, 1, true)
	ctx := context.Background()
	task, err := testHandler.TaskService.EnqueueTaskForIssueByActor(ctx, fix.issue(t), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("enqueue Role: %v", err)
	}
	if !task.RuntimeID.Valid {
		t.Fatal("expected a stamped row")
	}
	// A stamped (running) row is not a restamp candidate: the sweep must leave
	// its tier/runtime exactly as enqueue wrote them.
	for _, row := range testHandler.TaskService.RestampRuntimelessTierTasks(ctx) {
		if uuidToString(row.ID) == uuidToString(task.ID) {
			t.Fatal("already stamped Role row was re-resolved")
		}
	}
	var tierID, runtimeID string
	dbfx.QueryRow(t, `SELECT COALESCE(tier_id::text,''), COALESCE(runtime_id::text,'') FROM agent_task_queue WHERE id = $1`, uuidToString(task.ID)).
		Scan(&tierID, &runtimeID)
	if tierID != uuidToString(task.TierID) || runtimeID != uuidToString(task.RuntimeID) {
		t.Fatalf("stamped row changed: tier=%s runtime=%s", tierID, runtimeID)
	}
}
