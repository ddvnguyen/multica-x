package handler

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
)

var roleTestNameSeq atomic.Int64

func roleTestName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), roleTestNameSeq.Add(1))
}

func roleBody(name string, extra map[string]any) map[string]any {
	body := map[string]any{"name": name, "subtype": "role", "tier_role": "architect"}
	for key, value := range extra {
		body[key] = value
	}
	return body
}

// cleanupCreatedAgent keeps the shared test workspace clean: an API-created
// agent owned by a test user would otherwise keep that user (and its member
// row) alive after the test, skewing count-based tests elsewhere in the suite.
func cleanupCreatedAgent(t *testing.T, agentID string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
}

func createRole(t *testing.T, body map[string]any) AgentResponse {
	t.Helper()
	created := testutil.Decode[AgentResponse](t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", body), http.StatusCreated)
	cleanupCreatedAgent(t, created.ID)
	return created
}

func createAgentWithRoleLink(t *testing.T, name, roleID string) AgentResponse {
	t.Helper()
	created := testutil.Decode[AgentResponse](t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":               name,
			"runtime_id":         testRuntimeID,
			"implements_role_id": roleID,
		}), http.StatusCreated)
	cleanupCreatedAgent(t, created.ID)
	return created
}

func readRoleTestAgent(t *testing.T, agentID string) AgentResponse {
	t.Helper()
	return testutil.Decode[AgentResponse](t, testHandler.GetAgent,
		withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID), http.StatusOK)
}

func updateAgentResponse(t *testing.T, agentID string, body map[string]any) AgentResponse {
	t.Helper()
	return testutil.Decode[AgentResponse](t, testHandler.UpdateAgent,
		withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, body), "id", agentID), http.StatusOK)
}

// TestCreateRole_FlagOffDenied proves the whole Role surface is dark by
// default: subtype, tier_role and implements_role_id cannot be used at all.
func TestCreateRole_FlagOffDenied(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)

	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", roleBody(roleTestName("role"), nil))).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":               roleTestName("agent"),
			"runtime_id":         testRuntimeID,
			"implements_role_id": "00000000-0000-0000-0000-000000000001",
		})).Want(http.StatusForbidden)
}

// TestRoleSubtypeRoundtrip covers create/get/update of a model-free Role and
// an Agent that implements it, including explicit NULL clears.
func TestRoleSubtypeRoundtrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	role := createRole(t, roleBody(roleTestName("role"), map[string]any{
		"description":  "owner-managed review role",
		"instructions": "Review carefully",
	}))
	if role.Subtype != "role" || role.TierRole != "architect" {
		t.Fatalf("created role subtype=%q tier_role=%q", role.Subtype, role.TierRole)
	}
	if role.RuntimeBound || role.Model != "" || role.ImplementsRoleID != nil {
		t.Fatalf("role carries runtime/model/link: bound=%v model=%q link=%v", role.RuntimeBound, role.Model, role.ImplementsRoleID)
	}
	var runtimeIDValid bool
	if err := testPool.QueryRow(context.Background(), `SELECT runtime_id IS NOT NULL FROM agent WHERE id = $1`, role.ID).Scan(&runtimeIDValid); err != nil {
		t.Fatalf("read role row: %v", err)
	}
	if runtimeIDValid {
		t.Fatal("role row has a runtime_id in the database")
	}

	agent := createAgentWithRoleLink(t, roleTestName("implementer"), role.ID)
	if agent.ImplementsRoleID == nil || *agent.ImplementsRoleID != role.ID {
		t.Fatalf("agent implements_role_id = %v, want %s", agent.ImplementsRoleID, role.ID)
	}
	if agent.Subtype != "agent" {
		t.Fatalf("implementing agent subtype = %q", agent.Subtype)
	}

	gotRole := readRoleTestAgent(t, role.ID)
	if gotRole.TierRole != "architect" || gotRole.Subtype != "role" {
		t.Fatalf("GetAgent role: subtype=%q tier_role=%q", gotRole.Subtype, gotRole.TierRole)
	}
	gotAgent := readRoleTestAgent(t, agent.ID)
	if gotAgent.ImplementsRoleID == nil || *gotAgent.ImplementsRoleID != role.ID {
		t.Fatalf("GetAgent agent implements_role_id = %v", gotAgent.ImplementsRoleID)
	}

	updatedRole := updateAgentResponse(t, role.ID, map[string]any{"tier_role": "engineer"})
	if updatedRole.TierRole != "engineer" {
		t.Fatalf("updated tier_role = %q", updatedRole.TierRole)
	}
	clearedRole := updateAgentResponse(t, role.ID, map[string]any{"tier_role": nil})
	if clearedRole.TierRole != "" {
		t.Fatalf("cleared tier_role = %q", clearedRole.TierRole)
	}
	clearedAgent := updateAgentResponse(t, agent.ID, map[string]any{"implements_role_id": nil})
	if clearedAgent.ImplementsRoleID != nil {
		t.Fatalf("cleared implements_role_id = %v", clearedAgent.ImplementsRoleID)
	}
}

// TestRoleValidationDenials covers the invariant checks: no model/runtime/
// account config on a Role, no role-to-role links, tier_role is Role-only,
// and a link must name a live Role in the same workspace.
func TestRoleValidationDenials(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	role := createRole(t, roleBody(roleTestName("role"), nil))
	for field, value := range map[string]any{
		"model":        "gpt-test",
		"runtime_id":   testRuntimeID,
		"account_mode": "specific",
		"use_tiers":    true,
		"tier_ids":     []string{},
		"role":         "engineer",
		"custom_env":   map[string]string{"KEY": "value"},
	} {
		testutil.Call(t, testHandler.CreateAgent,
			newRequest(http.MethodPost, "/api/agents", roleBody(roleTestName("role"), map[string]any{field: value}))).
			Want(http.StatusBadRequest)
	}
	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", roleBody(roleTestName("role"), map[string]any{
			"implements_role_id": role.ID,
		}))).Want(http.StatusBadRequest)

	// An ordinary agent cannot carry tier_role, and cannot link to an
	// ordinary agent as if it were a Role.
	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name": roleTestName("agent"), "runtime_id": testRuntimeID, "tier_role": "engineer",
		})).Want(http.StatusBadRequest)
	plain := createPlainAgent(t, roleTestName("plain"))
	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name": roleTestName("agent"), "runtime_id": testRuntimeID, "implements_role_id": plain,
		})).Want(http.StatusBadRequest)

	// A Role from a different workspace is rejected even by id.
	foreignRole := createForeignRole(t)
	testutil.Call(t, testHandler.CreateAgent,
		newRequest(http.MethodPost, "/api/agents", map[string]any{
			"name": roleTestName("agent"), "runtime_id": testRuntimeID, "implements_role_id": foreignRole,
		})).Want(http.StatusBadRequest)
}

// TestRolePrivilegeDenials covers workspace roles and visibility: only an
// owner/admin may author roles or link an Agent to one, and a private Role is
// not readable by a plain workspace member.
func TestRolePrivilegeDenials(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)

	memberID := createPermissionTestMember(t, fmt.Sprintf("role-member-%d@multica.ai", time.Now().UnixNano()))
	role := createRole(t, roleBody(roleTestName("role"), map[string]any{"permission_mode": "private"}))

	testutil.Call(t, testHandler.CreateAgent,
		newRequestAs(memberID, http.MethodPost, "/api/agents", roleBody(roleTestName("role"), nil))).
		Want(http.StatusForbidden)
	testutil.Call(t, testHandler.UpdateAgent,
		withURLParam(newRequestAs(memberID, http.MethodPut, "/api/agents/"+role.ID, map[string]any{"instructions": "nope"}), "id", role.ID)).
		Want(http.StatusForbidden)
	testutil.Call(t, testHandler.GetAgent,
		withURLParam(newRequestAs(memberID, http.MethodGet, "/api/agents/"+role.ID, nil), "id", role.ID)).
		Want(http.StatusForbidden)
	testutil.Call(t, testHandler.CreateAgent,
		newRequestAs(memberID, http.MethodPost, "/api/agents", map[string]any{
			"name": roleTestName("agent"), "runtime_id": testRuntimeID, "implements_role_id": role.ID,
		})).Want(http.StatusForbidden)

	// An admin may author and link.
	adminID := createPermissionTestAdmin(t, fmt.Sprintf("role-admin-%d@multica.ai", time.Now().UnixNano()))
	adminRole := testutil.Decode[AgentResponse](t, testHandler.CreateAgent,
		newRequestAs(adminID, http.MethodPost, "/api/agents", roleBody(roleTestName("role"), nil)), http.StatusCreated)
	cleanupCreatedAgent(t, adminRole.ID)
}

func createPlainAgent(t *testing.T, name string) string {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/agents", map[string]any{"name": name, "runtime_id": testRuntimeID})
	id := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated).ID
	cleanupCreatedAgent(t, id)
	return id
}

// createForeignRole inserts a Role in another workspace so cross-workspace
// link attempts can be exercised without depending on a second test fixture.
func createForeignRole(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', $3) RETURNING id
	`, roleTestName("foreign-ws"), roleTestName("foreign-ws"), "FROLE").Scan(&workspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	var roleID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, visibility, permission_mode, subtype, tier_role, owner_id)
		VALUES ($1, $2, 'cloud', 'private', 'private', 'role', 'architect', $3) RETURNING id
	`, workspaceID, roleTestName("foreign-role"), testUserID).Scan(&roleID); err != nil {
		t.Fatalf("insert foreign role: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, roleID)
	})
	return roleID
}
