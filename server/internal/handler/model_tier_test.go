package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── pure validation ──────────────────────────────────────────────────────────

func TestParseTierCost(t *testing.T) {
	valid := map[string]string{
		"0.05": "0.05", " 1.5 ": "1.5", "10": "10",
	}
	for in, want := range valid {
		got, err := parseTierCost(&in)
		if err != nil || !got.Valid {
			t.Errorf("parseTierCost(%q) err = %v, want valid", in, err)
			continue
		}
		f, _ := got.Float64Value()
		if fmt.Sprintf("%v", f.Float64) != want {
			t.Errorf("parseTierCost(%q) = %v, want %s", in, f.Float64, want)
		}
	}
	empty := ""
	if got, err := parseTierCost(&empty); err != nil || got.Valid {
		t.Errorf("empty cost should be NULL, got valid=%v err=%v", got.Valid, err)
	}
	if got, err := parseTierCost(nil); err != nil || got.Valid {
		t.Errorf("nil cost should be NULL, got valid=%v err=%v", got.Valid, err)
	}
	for _, bad := range []string{"abc", "NaN", "Inf", "1_000", ""} {
		if bad == "" {
			continue
		}
		got, err := parseTierCost(&bad)
		if err == nil || got.Valid {
			t.Errorf("parseTierCost(%q) should fail, got valid=%v", bad, got.Valid)
		}
	}
}

func TestNormalizeTierInput(t *testing.T) {
	str := func(s string) *string { return &s }
	intp := func(n int) *int { return &n }
	falsep := func() *bool { b := false; return &b }

	// Create defaults: idx 99, slots 1, enabled stays false unless set —
	// the caller (handler) passes Enabled explicitly on create.
	in := modelTierInput{Name: str("hard"), Provider: str("codebuddy"), Model: str("m1")}
	params, _, err := normalizeTierInput(in, nil, true)
	if err != nil {
		t.Fatalf("create err = %v", err)
	}
	if params.Idx != 99 || params.ProviderSlots != 1 {
		t.Fatalf("create defaults = idx %d slots %d, want 99/1", params.Idx, params.ProviderSlots)
	}

	// Create validation.
	for name, mut := range map[string]func(*modelTierInput){
		"empty name":     func(i *modelTierInput) { i.Name = str("  ") },
		"missing name":   func(i *modelTierInput) { i.Name = nil },
		"missing model":  func(i *modelTierInput) { i.Model = nil },
		"missing prover": func(i *modelTierInput) { i.Provider = nil },
		"idx low":        func(i *modelTierInput) { i.Idx = intp(-1) },
		"idx high":       func(i *modelTierInput) { i.Idx = intp(100) },
		"bad quality":    func(i *modelTierInput) { i.Quality = intp(101) },
		"zero slots":     func(i *modelTierInput) { i.ProviderSlots = intp(0) },
		"bad runtime":    func(i *modelTierInput) { i.RuntimeID = str("nope") },
		"bad cost":       func(i *modelTierInput) { i.Cost = str("much") },
	} {
		base := modelTierInput{Name: str("t"), Provider: str("p"), Model: str("m")}
		mut(&base)
		if _, _, err := normalizeTierInput(base, nil, true); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	// Update keeps omitted fields; explicit empty runtime_id unbinds.
	// Round-trip through the DB row shape: normalizeTierInput reads a ModelTier.
	curRow := db.ModelTier{Name: params.Name, Idx: params.Idx, RuntimeID: params.RuntimeID, Provider: params.Provider, Model: params.Model, ThinkingLevel: params.ThinkingLevel, ServiceTier: params.ServiceTier, Cost: params.Cost, Quality: params.Quality, ProviderSlots: params.ProviderSlots, Enabled: true}
	upd := modelTierInput{Enabled: falsep(), RuntimeID: str("")}
	got, _, err := normalizeTierInput(upd, &curRow, false)
	if err != nil {
		t.Fatalf("update err = %v", err)
	}
	if got.Name != "hard" || got.Enabled || got.RuntimeID.Valid {
		t.Fatalf("update = %+v, want name kept, disabled, runtime unbound", got)
	}
}

// ── database-backed ──────────────────────────────────────────────────────────

func newTierTestEnv(t *testing.T) (foreignWorkspaceID, memberUserID string) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'WTR') RETURNING id`, "tier-foreign-"+suffix, "tier-foreign-"+suffix).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Tier Member', $1) RETURNING id`, "tier-member-"+suffix+"@multica.test").Scan(&memberUserID); err != nil {
		t.Fatalf("create member user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberUserID) })
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, memberUserID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM model_tier WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM model_tier WHERE workspace_id = $1`, foreignWorkspaceID)
	})
	return foreignWorkspaceID, memberUserID
}

func createTierAs(t *testing.T, userID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := withURLParam(newRequestAs(userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tiers", body), "id", testWorkspaceID)
	return testutil.Call(t, testHandler.CreateModelTier, req).ResponseRecorder
}

func mustCreateTier(t *testing.T, body map[string]any) ModelTierResponse {
	t.Helper()
	var tier ModelTierResponse
	testutil.Call(t, testHandler.CreateModelTier, withURLParam(newRequest(
		http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/tiers", body), "id", testWorkspaceID)).
		Want(http.StatusCreated).JSON(&tier)
	return tier
}

func listTiersAs(t *testing.T, userID string) []ModelTierResponse {
	t.Helper()
	var out struct {
		Tiers []ModelTierResponse `json:"tiers"`
	}
	testutil.Call(t, testHandler.ListModelTiers, withURLParam(
		newRequestAs(userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/tiers", nil), "id", testWorkspaceID)).
		Want(http.StatusOK).JSON(&out)
	return out.Tiers
}

func TestModelTierCRUDAndAuthz(t *testing.T) {
	_, memberUserID := newTierTestEnv(t)

	// Plain members cannot write.
	if rec := createTierAs(t, memberUserID, map[string]any{"name": "x", "provider": "p", "model": "m"}); rec.Code != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403: %s", rec.Code, rec.Body)
	}

	// Create validation.
	for name, body := range map[string]map[string]any{
		"empty name":    {"name": "  ", "provider": "p", "model": "m"},
		"missing model": {"name": "x", "provider": "p"},
		"idx out":       {"name": "x", "provider": "p", "model": "m", "idx": 100},
		"zero slots":    {"name": "x", "provider": "p", "model": "m", "provider_slots": 0},
		"bad cost":      {"name": "x", "provider": "p", "model": "m", "cost": "much"},
		"bad runtime":   {"name": "x", "provider": "p", "model": "m", "runtime_id": "not-a-uuid"},
	} {
		if rec := createTierAs(t, testUserID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body)
		}
	}

	a := mustCreateTier(t, map[string]any{
		"name": "hard", "provider": "codebuddy", "model": "m1",
		"idx": 0, "cost": 0.05, "quality": 90, "provider_slots": 1,
	})
	if a.Cost == nil || *a.Cost != "0.05" || a.Quality == nil || *a.Quality != 90 {
		t.Fatalf("created tier = %+v", a)
	}
	b := mustCreateTier(t, map[string]any{"name": "cheap", "provider": "p", "model": "m2"})

	// Listing is idx-ordered and member-visible.
	got := listTiersAs(t, memberUserID)
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID {
		t.Fatalf("member list = %+v", got)
	}

	// Update: partial; member forbidden; unknown tier 404.
	upd := func(userID, tierID string, body map[string]any) *httptest.ResponseRecorder {
		req := withURLParams(newRequestAs(userID, http.MethodPut, "/x", body),
			"id", testWorkspaceID, "tierId", tierID)
		return testutil.Call(t, testHandler.UpdateModelTier, req).ResponseRecorder
	}
	if rec := upd(memberUserID, a.ID, map[string]any{"enabled": false}); rec.Code != http.StatusForbidden {
		t.Fatalf("member update = %d, want 403", rec.Code)
	}
	if rec := upd(testUserID, "11111111-1111-1111-1111-111111111111", map[string]any{"enabled": false}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tier update = %d, want 404", rec.Code)
	}
	if rec := upd(testUserID, a.ID, map[string]any{"idx": -1}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad idx update = %d, want 400", rec.Code)
	}
	var disabled ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequest(http.MethodPut, "/x", map[string]any{"enabled": false, "quality": 10}),
		"id", testWorkspaceID, "tierId", a.ID)).Want(http.StatusOK).JSON(&disabled)
	if disabled.Enabled || disabled.Quality == nil || *disabled.Quality != 10 || disabled.Name != "hard" {
		t.Fatalf("updated tier = %+v", disabled)
	}

	// Reorder: full ordered list, position becomes idx; member forbidden.
	reorder := func(userID string, ids []string) *httptest.ResponseRecorder {
		req := withURLParam(newRequestAs(userID, http.MethodPut,
			"/api/workspaces/"+testWorkspaceID+"/tiers/reorder", map[string]any{"ids": ids}), "id", testWorkspaceID)
		return testutil.Call(t, testHandler.ReorderModelTiers, req).ResponseRecorder
	}
	if rec := reorder(memberUserID, []string{b.ID, a.ID}); rec.Code != http.StatusForbidden {
		t.Fatalf("member reorder = %d, want 403", rec.Code)
	}
	if rec := reorder(testUserID, []string{a.ID, a.ID}); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate reorder = %d, want 400", rec.Code)
	}
	if rec := reorder(testUserID, []string{a.ID, "11111111-1111-1111-1111-111111111111"}); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign id reorder = %d, want 404", rec.Code)
	}
	var reordered struct {
		Tiers []ModelTierResponse `json:"tiers"`
	}
	testutil.Call(t, testHandler.ReorderModelTiers, withURLParam(newRequest(
		http.MethodPut, "/x", map[string]any{"ids": []string{b.ID, a.ID}}), "id", testWorkspaceID)).
		Want(http.StatusOK).JSON(&reordered)
	if len(reordered.Tiers) != 2 || reordered.Tiers[0].ID != b.ID || reordered.Tiers[0].Idx != 0 || reordered.Tiers[1].Idx != 1 {
		t.Fatalf("reordered = %+v", reordered.Tiers)
	}

	// Delete: member forbidden, then real delete 204 and list shrinks.
	del := func(userID, tierID string) *httptest.ResponseRecorder {
		req := withURLParams(newRequestAs(userID, http.MethodDelete, "/x", nil),
			"id", testWorkspaceID, "tierId", tierID)
		return testutil.Call(t, testHandler.DeleteModelTier, req).ResponseRecorder
	}
	if rec := del(memberUserID, b.ID); rec.Code != http.StatusForbidden {
		t.Fatalf("member delete = %d, want 403", rec.Code)
	}
	if rec := del(testUserID, "11111111-1111-1111-1111-111111111111"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tier delete = %d, want 404", rec.Code)
	}
	if rec := del(testUserID, b.ID); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body)
	}
	if got := listTiersAs(t, testUserID); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("after delete list = %+v", got)
	}
}

func TestModelTierRuntimeScoping(t *testing.T) {
	foreignWorkspaceID, _ := newTierTestEnv(t)
	ctx := context.Background()

	var foreignRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'tier-foreign-machine', 'tier-foreign-rt', 'local', 'codebuddy', 'online', 'x', '{}'::jsonb, now(), 'public', $2)
		RETURNING id`, foreignWorkspaceID, testUserID).Scan(&foreignRuntimeID); err != nil {
		t.Fatalf("create foreign runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, foreignRuntimeID)
	})

	// A tier must never point at another workspace's runtime.
	rec := createTierAs(t, testUserID, map[string]any{
		"name": "x", "provider": "p", "model": "m", "runtime_id": foreignRuntimeID,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign runtime = %d, want 400: %s", rec.Code, rec.Body)
	}

	// A tier without a runtime is fine (usable once M-351 resolves).
	tier := mustCreateTier(t, map[string]any{"name": "norun", "provider": "p", "model": "m"})
	if tier.RuntimeID != "" {
		t.Fatalf("runtime-less tier = %+v", tier)
	}
}
