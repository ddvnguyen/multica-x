package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Role registry management (M-498): the tabs are built from this list, so an
// admin creates, renames and removes roles here. Rename carries the per-role
// rows with the key; delete is refused while a tier still uses it.

func roleListAs(t *testing.T, userID string) []TierRoleResponse {
	t.Helper()
	var out struct {
		Roles []TierRoleResponse `json:"roles"`
	}
	testutil.Call(t, testHandler.ListTierRoles, withURLParam(
		newRequestAs(userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/tiers/roles", nil), "id", testWorkspaceID)).
		Want(http.StatusOK).JSON(&out)
	return out.Roles
}

func createRoleAs(t *testing.T, userID, key string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.CreateTierRole, withURLParam(
		newRequestAs(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tiers/roles",
			map[string]any{"key": key}), "id", testWorkspaceID))
}

func renameRoleAs(t *testing.T, userID, from, to string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.RenameTierRole, withURLParams(
		newRequestAs(userID, http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/tiers/roles/"+from,
			map[string]any{"key": to}), "id", testWorkspaceID, "roleKey", from))
}

func deleteRoleAs(t *testing.T, userID, key string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.DeleteTierRole, withURLParams(
		newRequestAs(userID, http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/tiers/roles/"+key, nil),
		"id", testWorkspaceID, "roleKey", key))
}

func roleKeys(roles []TierRoleResponse) []string {
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		out = append(out, role.Key)
	}
	return out
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func TestTierRoleRegistryCreateAndList(t *testing.T) {
	_, memberUserID := newTierTestEnv(t)
	admin := testUserID

	// The three classic roles are seeded for every workspace (migration 744).
	keys := roleKeys(roleListAs(t, admin))
	for _, classic := range []string{"architect", "management", "engineer"} {
		if !containsKey(keys, classic) {
			t.Fatalf("seeded roles = %v, want %s", keys, classic)
		}
	}

	createRoleAs(t, admin, "qa").Want(http.StatusOK)
	if keys := roleKeys(roleListAs(t, admin)); !containsKey(keys, "qa") {
		t.Fatalf("roles after create = %v, want qa", keys)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM tier_role WHERE workspace_id = $1 AND key = $2`, testWorkspaceID, "qa")
	})

	// A duplicate key is a conflict, not a second tab.
	createRoleAs(t, admin, "qa").Want(http.StatusConflict)

	// Shape is enforced the same way tier roles are.
	for _, bad := range []string{"", "QA", "qa role", "-qa", "qa_role"} {
		createRoleAs(t, admin, bad).Want(http.StatusBadRequest)
	}

	// Plain members read the registry but cannot change it.
	createRoleAs(t, memberUserID, "member-made").Want(http.StatusForbidden)
	renameRoleAs(t, memberUserID, "qa", "qa2").Want(http.StatusForbidden)
	deleteRoleAs(t, memberUserID, "qa").Want(http.StatusForbidden)
}

func TestTierRoleRegistryRenameCarriesTheTiers(t *testing.T) {
	_, _ = newTierTestEnv(t)
	admin := testUserID

	created := mustCreateTier(t, map[string]any{
		"name": "role-rename", "provider": "freebuff", "model": "m1",
		"roles": []string{"engineer"}, "enabled": true,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM model_tier_role WHERE tier_id = $1`, created.ID)
	})

	renameRoleAs(t, admin, "engineer", "builder").Want(http.StatusOK)
	t.Cleanup(func() {
		// Put the seeded role back: the test workspace is shared by the whole
		// package, so a later test still finds the classic three tabs.
		testPool.Exec(context.Background(), `DELETE FROM tier_role WHERE workspace_id = $1 AND key = 'builder'`, testWorkspaceID)
		testPool.Exec(context.Background(), `INSERT INTO tier_role (workspace_id, key, position) VALUES ($1, 'engineer', 2) ON CONFLICT DO NOTHING`, testWorkspaceID)
		testPool.Exec(context.Background(), `UPDATE model_tier_role SET role = 'engineer' WHERE tier_id = $1 AND role = 'builder'`, created.ID)
	})

	// The tier keeps its place in the renamed role's list, and the old key is
	// gone from the registry.
	keys := roleKeys(roleListAs(t, admin))
	if containsKey(keys, "engineer") || !containsKey(keys, "builder") {
		t.Fatalf("roles after rename = %v, want builder and no engineer", keys)
	}
	var role string
	if err := testPool.QueryRow(context.Background(),
		`SELECT role FROM model_tier_role WHERE tier_id = $1`, created.ID).Scan(&role); err != nil {
		t.Fatalf("read per-role row: %v", err)
	}
	if role != "builder" {
		t.Fatalf("per-role row = %q, want builder", role)
	}

	// Renaming onto an existing key is a conflict, and an unknown source is 404.
	renameRoleAs(t, admin, "builder", "management").Want(http.StatusConflict)
	renameRoleAs(t, admin, "nope", "anything").Want(http.StatusNotFound)
}

func TestTierRoleRegistryDeleteRefusesARoleInUse(t *testing.T) {
	_, _ = newTierTestEnv(t)
	admin := testUserID

	created := mustCreateTier(t, map[string]any{
		"name": "role-delete", "provider": "freebuff", "model": "m1",
		"roles": []string{"engineer"}, "enabled": true,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM model_tier_role WHERE tier_id = $1`, created.ID)
	})

	rec := deleteRoleAs(t, admin, "engineer").Want(http.StatusConflict)
	if body := rec.Body.String(); !strings.Contains(body, "role_in_use") {
		t.Fatalf("delete in-use body = %s, want role_in_use", body)
	}

	// An unused role deletes cleanly, then is 404 on a second attempt.
	createRoleAs(t, admin, "temp").Want(http.StatusOK)
	deleteRoleAs(t, admin, "temp").Want(http.StatusOK)
	if keys := roleKeys(roleListAs(t, admin)); containsKey(keys, "temp") {
		t.Fatalf("roles after delete = %v, want no temp", keys)
	}
	deleteRoleAs(t, admin, "temp").Want(http.StatusNotFound)
}

func TestReorderRejectsTiersOutsideTheRole(t *testing.T) {
	_, _ = newTierTestEnv(t)

	inside := mustCreateTier(t, map[string]any{
		"name": "reorder-in", "provider": "freebuff", "model": "m1",
		"roles": []string{"engineer"}, "enabled": true,
	})
	outside := mustCreateTier(t, map[string]any{
		"name": "reorder-out", "provider": "freebuff", "model": "m2",
		"roles": []string{"architect"}, "enabled": true,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM model_tier_role WHERE tier_id IN ($1, $2)`, inside.ID, outside.ID)
	})

	testutil.Call(t, testHandler.ReorderModelTiers, withURLParam(
		newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/tiers/reorder",
			map[string]any{"ids": []string{outside.ID}, "role": "engineer"}), "id", testWorkspaceID)).
		Want(http.StatusBadRequest)

	// The role's own list still reorders.
	testutil.Call(t, testHandler.ReorderModelTiers, withURLParam(
		newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/tiers/reorder",
			map[string]any{"ids": []string{inside.ID}, "role": "engineer"}), "id", testWorkspaceID)).
		Want(http.StatusOK)
}

// M-517 fix: ListTierRoles carries a workspace-scoped agent_role_type
// boolean so the UI gates its Role surfaces per workspace. The roles list
// itself stays member-visible; only the boolean follows the flag.
func TestListTierRolesReportsWorkspaceScopedRoleFlag(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	listRolesAs := func(userID string) map[string]any {
		t.Helper()
		var out map[string]any
		testutil.Call(t, testHandler.ListTierRoles, withURLParam(
			newRequestAs(userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/tiers/roles", nil), "id", testWorkspaceID)).
			Want(http.StatusOK).JSON(&out)
		return out
	}

	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	if got := listRolesAs(testUserID)["agent_role_type"]; got != false {
		t.Fatalf("flag off must report agent_role_type=false, got %v", got)
	}

	// allow_by: workspace_id targets only the test workspace.
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.AgentRoleType, featureflag.Rule{
		Default: false,
		AllowBy: "workspace_id",
		Allow:   []string{testWorkspaceID},
	})
	orig := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = orig })
	if got := listRolesAs(testUserID)["agent_role_type"]; got != true {
		t.Fatalf("targeted workspace must report agent_role_type=true, got %v", got)
	}

	// Roles still list with the flag off: Settings Tiers and tier pickers
	// must keep working.
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	if roles, ok := listRolesAs(testUserID)["roles"].([]any); !ok || roles == nil {
		t.Fatalf("roles list must stay present with the flag off, got %v", listRolesAs(testUserID)["roles"])
	}
}
