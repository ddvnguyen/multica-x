package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// patchAgentSubtype sends the M-567 type switch through the real handler.
func patchAgentSubtype(t *testing.T, agentID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID)
	testHandler.UpdateAgent(w, req)
	return w
}

func decodePatchAgent(t *testing.T, agentID string, body map[string]any) AgentResponse {
	t.Helper()
	return testutil.Decode[AgentResponse](t, testHandler.UpdateAgent,
		withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, body), "id", agentID),
		http.StatusOK)
}

func storedAgentFields(t *testing.T, agentID string) (subtype, tierRole, runtimeID, model string) {
	t.Helper()
	var nullableTierRole, nullableRuntimeID, nullableModel *string
	err := testPool.QueryRow(context.Background(), `
		SELECT subtype, tier_role, runtime_id::text, model FROM agent WHERE id = $1
	`, agentID).Scan(&subtype, &nullableTierRole, &nullableRuntimeID, &nullableModel)
	if err != nil {
		t.Fatalf("read stored agent %s: %v", agentID, err)
	}
	if nullableTierRole != nil {
		tierRole = *nullableTierRole
	}
	if nullableRuntimeID != nil {
		runtimeID = *nullableRuntimeID
	}
	if nullableModel != nil {
		model = *nullableModel
	}
	return
}

func countTypeChangeActivities(t *testing.T, agentID string) int {
	t.Helper()
	var count int
	err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM activity_log
		WHERE action = 'agent_type_changed' AND details->>'agent_id' = $1
	`, agentID).Scan(&count)
	if err != nil {
		t.Fatalf("count agent_type_changed activities: %v", err)
	}
	return count
}

// TestAgentTypeConversion_AgentToRole proves the conversion clears the
// agent's own Role link (a Role cannot implement a Role) while keeping the
// runtime/model columns so switching back restores them.
func TestAgentTypeConversion_AgentToRole(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	role := createRole(t, roleBody(roleTestName("role"), nil))
	agent := createAgentWithRoleLink(t, roleTestName("agent-to-role"), role.ID)
	if agent.ImplementsRoleID == nil {
		t.Fatal("setup: implementing agent has no Role link")
	}

	updated := decodePatchAgent(t, agent.ID, map[string]any{
		"subtype": "role", "tier_role": "engineer",
	})
	if updated.Subtype != "role" {
		t.Fatalf("subtype = %q, want role", updated.Subtype)
	}
	if updated.TierRole != "engineer" {
		t.Fatalf("tier_role = %q, want engineer", updated.TierRole)
	}
	if updated.ImplementsRoleID != nil {
		t.Fatalf("implementing Role link not cleared: %v", updated.ImplementsRoleID)
	}

	subtype, tierRole, runtimeID, _ := storedAgentFields(t, agent.ID)
	if subtype != "role" || tierRole != "engineer" {
		t.Fatalf("stored subtype=%q tier_role=%q", subtype, tierRole)
	}
	if runtimeID == "" {
		t.Fatal("conversion dropped the stored runtime_id; switching back could not restore it")
	}
	if got := countTypeChangeActivities(t, agent.ID); got != 1 {
		t.Fatalf("activity rows = %d, want exactly 1", got)
	}
}

// TestAgentTypeConversion_RoleToAgent_NoDependents proves a Role with no
// implementers converts back, keeping its tier_role column inactive.
func TestAgentTypeConversion_RoleToAgent_NoDependents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	role := createRole(t, roleBody(roleTestName("role"), map[string]any{"tier_role": "engineer"}))
	updated := decodePatchAgent(t, role.ID, map[string]any{"subtype": "agent"})
	if updated.Subtype != "agent" {
		t.Fatalf("subtype = %q, want agent", updated.Subtype)
	}
	subtype, tierRole, runtimeID, _ := storedAgentFields(t, role.ID)
	if subtype != "agent" {
		t.Fatalf("stored subtype = %q, want agent", subtype)
	}
	if tierRole != "engineer" {
		t.Fatalf("inactive tier_role was lost: %q", tierRole)
	}
	if runtimeID != "" {
		t.Fatalf("converted Agent should need a runtime, got %q", runtimeID)
	}
	if got := countTypeChangeActivities(t, role.ID); got != 1 {
		t.Fatalf("activity rows = %d, want exactly 1", got)
	}
}

// TestAgentTypeConversion_RoleToAgent_WithDependents proves the guard returns
// the count and names of the Agents that still implement the Role, and that
// nothing was written on the conflict path.
func TestAgentTypeConversion_RoleToAgent_WithDependents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	role := createRole(t, roleBody(roleTestName("role"), nil))
	first := createAgentWithRoleLink(t, roleTestName("implementer-one"), role.ID)
	second := createAgentWithRoleLink(t, roleTestName("implementer-two"), role.ID)

	w := patchAgentSubtype(t, role.ID, map[string]any{"subtype": "agent"})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "2 agents implement this Role") {
		t.Fatalf("409 body missing dependent count: %s", body)
	}
	if !strings.Contains(body, first.Name) || !strings.Contains(body, second.Name) {
		t.Fatalf("409 body missing implementer names: %s", body)
	}
	if subtype, _, _, _ := storedAgentFields(t, role.ID); subtype != "role" {
		t.Fatalf("conflicting conversion changed stored subtype to %q", subtype)
	}
	if got := countTypeChangeActivities(t, role.ID); got != 0 {
		t.Fatalf("conflicting conversion wrote %d activity rows, want 0", got)
	}
}

// TestAgentTypeConversion_BlockedByActiveTask covers the queued/running guard
// in both directions and in both conversion directions.
func TestAgentTypeConversion_BlockedByActiveTask(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	for _, status := range []string{"queued", "running"} {
		t.Run(status, func(t *testing.T) {
			role := createRole(t, roleBody(roleTestName("role"), nil))
			agentID := dbfx.Agent(t, roleTestName("busy-agent"), testRuntimeID)
			for _, target := range []struct {
				id       string
				subtype  string
				wantKeep string
			}{
				{agentID, "role", "agent"},
				{role.ID, "agent", "role"},
			} {
				issueID := dbfx.Issue(t, roleTestName("busy-issue"))
				cols := testutil.Cols{"issue_id": issueID, "runtime_id": testRuntimeID, "status": status}
				if status == "running" {
					cols["dispatched_at"] = testutil.Raw("now()")
					cols["started_at"] = testutil.Raw("now()")
				}
				dbfx.Task(t, target.id, cols)

				w := patchAgentSubtype(t, target.id, map[string]any{"subtype": target.subtype})
				if w.Code != http.StatusConflict {
					t.Fatalf("%s → %s with %s task: expected 409, got %d: %s",
						target.wantKeep, target.subtype, status, w.Code, w.Body.String())
				}
				if !strings.Contains(w.Body.String(), "wait for runs to finish") {
					t.Fatalf("409 body missing run guard: %s", w.Body.String())
				}
				if subtype, _, _, _ := storedAgentFields(t, target.id); subtype != target.wantKeep {
					t.Fatalf("stored subtype changed to %q, want %q", subtype, target.wantKeep)
				}
			}
		})
	}
}

func TestAgentTypeConversion_FlagOffDenied(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)

	agentID := dbfx.Agent(t, roleTestName("flag-off-agent"), testRuntimeID)
	w := patchAgentSubtype(t, agentID, map[string]any{"subtype": "role"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("flag off: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if subtype, _, _, _ := storedAgentFields(t, agentID); subtype != "agent" {
		t.Fatalf("flag-off request changed subtype to %q", subtype)
	}
}

func TestAgentTypeConversion_IdempotentAndInvalid(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	agentID := dbfx.Agent(t, roleTestName("idempotent-agent"), testRuntimeID)
	if got := decodePatchAgent(t, agentID, map[string]any{"subtype": "agent"}); got.Subtype != "agent" {
		t.Fatalf("same-type agent PATCH: subtype = %q", got.Subtype)
	}
	role := createRole(t, roleBody(roleTestName("idempotent-role"), nil))
	if got := decodePatchAgent(t, role.ID, map[string]any{"subtype": "role"}); got.Subtype != "role" {
		t.Fatalf("same-type role PATCH: subtype = %q", got.Subtype)
	}
	if got := countTypeChangeActivities(t, agentID); got != 0 {
		t.Fatalf("same-type PATCH wrote %d activity rows, want 0", got)
	}
	if got := countTypeChangeActivities(t, role.ID); got != 0 {
		t.Fatalf("same-type PATCH wrote %d role activity rows, want 0", got)
	}

	w := patchAgentSubtype(t, agentID, map[string]any{"subtype": "wizard"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid subtype: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
