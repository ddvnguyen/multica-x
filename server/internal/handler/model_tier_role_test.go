package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// ── M-497: per-role index ────────────────────────────────────────────────────

func listTiersWithRole(t *testing.T, userID, role string) ([]ModelTierResponse, int) {
	t.Helper()
	var out struct {
		Tiers []ModelTierResponse `json:"tiers"`
	}
	path := "/api/workspaces/" + testWorkspaceID + "/tiers"
	if role != "" {
		path += "?role=" + role
	}
	rec := testutil.Call(t, testHandler.ListModelTiers, withURLParam(
		newRequestAs(userID, http.MethodGet, path, nil), "id", testWorkspaceID)).ResponseRecorder
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode tiers: %v", err)
	}
	return out.Tiers, http.StatusOK
}

func reorderTiersWithRole(t *testing.T, ids []string, role string) ([]ModelTierResponse, int) {
	t.Helper()
	body := map[string]any{"ids": ids}
	if role != "" {
		body["role"] = role
	}
	var out struct {
		Tiers []ModelTierResponse `json:"tiers"`
	}
	rec := testutil.Call(t, testHandler.ReorderModelTiers, withURLParam(newRequest(
		http.MethodPut, "/x", body), "id", testWorkspaceID)).ResponseRecorder
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode reordered tiers: %v", err)
	}
	return out.Tiers, http.StatusOK
}

func roleIdxOf(tiers []ModelTierResponse) map[string]int {
	out := map[string]int{}
	for _, tr := range tiers {
		if tr.RoleIdx == nil {
			continue
		}
		out[tr.ID] = *tr.RoleIdx
	}
	return out
}

// TestModelTierRoleLists pins the ?role= contract: only tiers with a row for
// the role appear, ordered by the role-specific idx with role_idx stamped;
// the unfiltered list is unchanged and carries no role_idx.
func TestModelTierRoleLists(t *testing.T) {
	newTierTestEnv(t)
	a := mustCreateTier(t, map[string]any{
		"name": "rlist-a", "provider": "p", "model": "m1", "roles": []string{"engineer", "architect"},
	})
	b := mustCreateTier(t, map[string]any{
		"name": "rlist-b", "provider": "p", "model": "m2", "roles": []string{"engineer"},
	})
	legacy := mustCreateTier(t, map[string]any{
		"name": "rlist-legacy", "provider": "p", "model": "m3",
	})

	eng, code := listTiersWithRole(t, testUserID, "engineer")
	if code != http.StatusOK || len(eng) != 2 || eng[0].ID != a.ID || eng[1].ID != b.ID {
		t.Fatalf("engineer list = %+v code %d, want [a b]", eng, code)
	}
	if idx := roleIdxOf(eng); idx[a.ID] != 0 || idx[b.ID] != 1 {
		t.Fatalf("engineer role_idx = %+v, want a:0 b:1", idx)
	}
	arch, _ := listTiersWithRole(t, testUserID, "architect")
	if len(arch) != 1 || arch[0].ID != a.ID || arch[0].RoleIdx == nil || *arch[0].RoleIdx != 0 {
		t.Fatalf("architect list = %+v, want [a] role_idx 0", arch)
	}
	mgmt, _ := listTiersWithRole(t, testUserID, "management")
	if len(mgmt) != 0 {
		t.Fatalf("management list = %+v, want empty", mgmt)
	}
	all, _ := listTiersWithRole(t, testUserID, "")
	if len(all) != 3 {
		t.Fatalf("unfiltered list = %d tiers, want 3", len(all))
	}
	for _, tr := range all {
		if tr.RoleIdx != nil {
			t.Fatalf("unfiltered tier %s carries role_idx %d, want omitted", tr.ID, *tr.RoleIdx)
		}
	}
	_ = legacy
	if _, code := listTiersWithRole(t, testUserID, "reviewer"); code != http.StatusBadRequest {
		t.Fatalf("legacy ?role=reviewer = %d, want 400", code)
	}
}

// TestModelTierRoleReorderIsolation pins the core promise: reordering one
// role leaves the other roles' order untouched.
func TestModelTierRoleReorderIsolation(t *testing.T) {
	newTierTestEnv(t)
	a := mustCreateTier(t, map[string]any{
		"name": "riso-a", "provider": "p", "model": "m1", "roles": []string{"engineer", "architect"},
	})
	b := mustCreateTier(t, map[string]any{
		"name": "riso-b", "provider": "p", "model": "m2", "roles": []string{"engineer", "architect"},
	})

	reordered, code := reorderTiersWithRole(t, []string{b.ID, a.ID}, "engineer")
	if code != http.StatusOK || len(reordered) != 2 || reordered[0].ID != b.ID || reordered[1].ID != a.ID {
		t.Fatalf("engineer reorder = %+v code %d, want [b a]", reordered, code)
	}
	if idx := roleIdxOf(reordered); idx[b.ID] != 0 || idx[a.ID] != 1 {
		t.Fatalf("reordered role_idx = %+v, want b:0 a:1", idx)
	}
	arch, _ := listTiersWithRole(t, testUserID, "architect")
	if len(arch) != 2 || arch[0].ID != a.ID || arch[1].ID != b.ID {
		t.Fatalf("architect list after engineer reorder = %+v, want [a b] unchanged", arch)
	}
	if _, code := reorderTiersWithRole(t, []string{a.ID}, "leader"); code != http.StatusBadRequest {
		t.Fatalf("reorder legacy role = %d, want 400", code)
	}
}

// TestModelTierRoleSyncOnUpdate pins role-row maintenance: added roles
// append at the end of their list, removed roles drop their rows, and an
// emptied set leaves every role list but keeps the unfiltered row.
func TestModelTierRoleSyncOnUpdate(t *testing.T) {
	newTierTestEnv(t)
	first := mustCreateTier(t, map[string]any{
		"name": "rsync-first", "provider": "p", "model": "m1", "roles": []string{"management"},
	})
	x := mustCreateTier(t, map[string]any{
		"name": "rsync-x", "provider": "p", "model": "m2", "roles": []string{"engineer"},
	})
	_ = first
	var widened ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequest(http.MethodPut, "/x", map[string]any{"roles": []string{"engineer", "management"}}),
		"id", testWorkspaceID, "tierId", x.ID)).Want(http.StatusOK).JSON(&widened)
	if len(widened.Roles) != 2 || widened.Roles[0] != "engineer" || widened.Roles[1] != "management" {
		t.Fatalf("widened roles = %+v, want [engineer management]", widened.Roles)
	}
	mgmt, _ := listTiersWithRole(t, testUserID, "management")
	if len(mgmt) != 2 || mgmt[1].ID != x.ID {
		t.Fatalf("management list after widen = %+v, want x appended last", mgmt)
	}
	var narrowed ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequest(http.MethodPut, "/x", map[string]any{"roles": []string{}}),
		"id", testWorkspaceID, "tierId", x.ID)).Want(http.StatusOK).JSON(&narrowed)
	if len(narrowed.Roles) != 0 {
		t.Fatalf("narrowed roles = %+v, want []", narrowed.Roles)
	}
	eng, _ := listTiersWithRole(t, testUserID, "engineer")
	for _, tr := range eng {
		if tr.ID == x.ID {
			t.Fatalf("engineer list still contains emptied tier x: %+v", eng)
		}
	}
	all, _ := listTiersWithRole(t, testUserID, "")
	found := false
	for _, tr := range all {
		if tr.ID == x.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("unfiltered list lost the emptied tier x: %+v", all)
	}
}

// TestModelTierFreeDerived pins M-497: is_free is derived from quota kinds —
// an explicit is_free:false alongside a free kind is ignored, and clearing
// the free kinds clears the flag.
func TestModelTierFreeDerived(t *testing.T) {
	newTierTestEnv(t)
	free := mustCreateTier(t, map[string]any{
		"name": "rfree", "provider": "p", "model": "m",
		"quota_kinds": []string{"free_daily"}, "is_free": false,
	})
	if !free.IsFree {
		t.Fatalf("tier with free_daily has is_free=false, want derived true")
	}
	paid := mustCreateTier(t, map[string]any{
		"name": "rpaid", "provider": "p", "model": "m", "quota_kinds": []string{"quota_daily"},
	})
	if paid.IsFree {
		t.Fatalf("tier with quota_daily has is_free=true, want false")
	}
	none := mustCreateTier(t, map[string]any{"name": "rnone", "provider": "p", "model": "m"})
	if none.IsFree {
		t.Fatalf("tier with no quota kinds has is_free=true, want false")
	}
	var unfreed ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequest(http.MethodPut, "/x", map[string]any{"quota_kinds": []string{"quota_weekly"}}),
		"id", testWorkspaceID, "tierId", free.ID)).Want(http.StatusOK).JSON(&unfreed)
	if unfreed.IsFree {
		t.Fatalf("tier after dropping free kinds still is_free=true, want false")
	}
}

// TestModelTierRoleBackfill runs the 741 backfill statement against a
// handler-bypassed row: one row per canonical role carrying the tier's idx,
// idempotent on rerun, and no rows for empty roles.
func TestModelTierRoleBackfill(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	newTierTestEnv(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var tierID, legacyID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO model_tier (workspace_id, name, idx, provider, model, provider_slots, enabled, roles)
		VALUES ($1, $2, 7, 'p', 'm', 1, true, '{engineer,management}') RETURNING id`,
		testWorkspaceID, "rbackfill-"+suffix).Scan(&tierID); err != nil {
		t.Fatalf("insert backfill tier: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO model_tier (workspace_id, name, idx, provider, model, provider_slots, enabled, roles)
		VALUES ($1, $2, 3, 'p', 'm', 1, true, '{}') RETURNING id`,
		testWorkspaceID, "rbackfill-legacy-"+suffix).Scan(&legacyID); err != nil {
		t.Fatalf("insert legacy tier: %v", err)
	}
	backfill := `
		INSERT INTO model_tier_role (workspace_id, tier_id, role, idx)
		SELECT t.workspace_id, t.id, r.role, t.idx
		FROM model_tier t
		CROSS JOIN LATERAL unnest(t.roles) AS r(role)
		WHERE r.role IN ('architect', 'management', 'engineer')
		  AND NOT EXISTS (
		      SELECT 1 FROM model_tier_role m
		      WHERE m.tier_id = t.id AND m.role = r.role
		  )`
	if _, err := testPool.Exec(ctx, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, err := testPool.Exec(ctx, backfill); err != nil {
		t.Fatalf("backfill rerun: %v", err)
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM model_tier_role WHERE tier_id = $1`, tierID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backfill rows = %d err %v, want 2", n, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM model_tier_role WHERE tier_id = $1 AND idx = 7`, tierID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("backfill rows at idx 7 = %d err %v, want 2", n, err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM model_tier_role WHERE tier_id = $1`, legacyID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy backfill rows = %d err %v, want 0", n, err)
	}
}

// ── pure helpers ─────────────────────────────────────────────────────────────

func TestCanonicalTierRoles(t *testing.T) {
	got := canonicalTierRoles([]string{"leader", "engineer", "planning", "engineer", "", "nope"})
	if len(got) != 3 || got[0] != "architect" || got[1] != "engineer" || got[2] != "management" {
		t.Fatalf("canonicalTierRoles = %+v, want [architect engineer management]", got)
	}
	if len(canonicalTierRoles(nil)) != 0 {
		t.Fatalf("canonicalTierRoles(nil) should be empty")
	}
}

func TestTierIsFreeFromKinds(t *testing.T) {
	for kinds, want := range map[[2]string]bool{
		{"free", ""}: true, {"free_daily", ""}: true,
		{"quota_daily", ""}: false, {"local", ""}: false,
	} {
		var ks []string
		for _, k := range kinds {
			if k != "" {
				ks = append(ks, k)
			}
		}
		if got := tierIsFreeFromKinds(ks); got != want {
			t.Fatalf("tierIsFreeFromKinds(%v) = %v, want %v", ks, got, want)
		}
	}
}
