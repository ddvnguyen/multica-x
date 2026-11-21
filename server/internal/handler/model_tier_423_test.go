package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-423 handler tests: new-field validation/normalization, CRUD round trip,
// and the anti-clear contract (admin 403s, owner passes).

func TestNormalizeTierInput423(t *testing.T) {
	str := func(s string) *string { return &s }

	// Create defaults: Go applies profile defaults (M-481).
	in := modelTierInput{Name: str("t"), Provider: str("p"), Model: str("m")}
	params, _, err := normalizeTierInput(in, nil, true)
	if err != nil {
		t.Fatalf("create err = %v", err)
	}
	if len(params.Roles) != 0 {
		t.Fatalf("create roles = %v, want empty", params.Roles)
	}
	if !params.AutoPick.Valid || !params.AutoPick.Bool {
		t.Fatalf("create auto_pick = %+v, want true", params.AutoPick)
	}
	if !params.ManualOnly.Valid || params.ManualOnly.Bool {
		t.Fatalf("create manual_only = %+v, want false", params.ManualOnly)
	}
	if !params.CtxMaxTokens.Valid || params.CtxMaxTokens.Int32 != 280000 {
		t.Fatalf("create ctx_max_tokens = %+v, want 280000", params.CtxMaxTokens)
	}
	if !params.CompactAtPct.Valid || params.CompactAtPct.Int32 != 75 {
		t.Fatalf("create compact_at_pct = %+v, want 75", params.CompactAtPct)
	}

	// Create validation.
	for name, mut := range map[string]func(*modelTierInput){
		"bad role":  func(i *modelTierInput) { i.RolesSet = true; i.Roles = []string{"ceo"} },
		"bad quota": func(i *modelTierInput) { i.QuotaKind = str("yearly") },
	} {
		base := modelTierInput{Name: str("t"), Provider: str("p"), Model: str("m")}
		mut(&base)
		if _, _, err := normalizeTierInput(base, nil, true); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	// Update keeps omitted fields; explicit [] clears roles.
	cur := db.ModelTier{Name: "t", Roles: []string{"engineer"}, ManualOnly: true, QuotaKind: "daily"}
	upd := modelTierInput{RolesSet: true, Roles: []string{}}
	got, _, err := normalizeTierInput(upd, &cur, false)
	if err != nil {
		t.Fatalf("update err = %v", err)
	}
	if len(got.Roles) != 0 || !got.ManualOnly.Valid || !got.ManualOnly.Bool || !got.QuotaKind.Valid || got.QuotaKind.String != "daily" {
		t.Fatalf("update = roles %v manual %+v quota %+v", got.Roles, got.ManualOnly, got.QuotaKind)
	}

	// tierManualProtectionWeakened unit pins.
	if tierManualProtectionWeakened(db.ModelTier{}, pgtype.Bool{}, false, nil) {
		t.Errorf("non-manual row must never weaken")
	}
	if !tierManualProtectionWeakened(db.ModelTier{ManualOnly: true}, pgtype.Bool{Bool: false, Valid: true}, false, nil) {
		t.Errorf("clearing manual_only weakens")
	}
	if !tierManualProtectionWeakened(db.ModelTier{ManualOnly: true, Roles: []string{"a", "b"}}, pgtype.Bool{Bool: true, Valid: true}, true, []string{"a"}) {
		t.Errorf("narrowing roles on a manual tier weakens")
	}
	if tierManualProtectionWeakened(db.ModelTier{ManualOnly: true, Roles: []string{"a"}}, pgtype.Bool{Bool: true, Valid: true}, false, nil) {
		t.Errorf("omitted roles must not weaken")
	}
	if tierManualProtectionWeakened(db.ModelTier{ManualOnly: true, Roles: []string{"a"}}, pgtype.Bool{Bool: true, Valid: true}, true, []string{"a", "b"}) {
		t.Errorf("widening roles must not weaken")
	}
}

// newTierAdminEnv mirrors newTierTestEnv but yields an admin member of the
// shared test workspace.
func newTierAdminEnv(t *testing.T) (adminUserID string) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ('Tier Admin', $1) RETURNING id`, "tier-admin-"+suffix+"@multica.test").Scan(&adminUserID); err != nil {
		t.Fatalf("create admin user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, adminUserID) })
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, testWorkspaceID, adminUserID); err != nil {
		t.Fatalf("add admin: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, adminUserID)
		testPool.Exec(context.Background(), `DELETE FROM model_tier WHERE workspace_id = $1`, testWorkspaceID)
	})
	return adminUserID
}

// TestModelTierManualProtection pins the CRUD anti-clear contract end to
// end: roles/quota/manual_only round-trip; an admin cannot clear manual_only,
// narrow a manual tier's roles, or delete it (403 each); the owner can.
func TestModelTierManualProtection(t *testing.T) {
	adminUserID := newTierAdminEnv(t)

	manual := mustCreateTier(t, map[string]any{
		"name": "opus", "provider": "p", "model": "m",
		"roles": []string{"architect"}, "manual_only": true, "quota_kind": "five_hour_weekly", "is_free": false,
	})
	if len(manual.Roles) != 1 || manual.Roles[0] != "architect" || !manual.ManualOnly || manual.QuotaKind != "five_hour_weekly" || manual.IsFree {
		t.Fatalf("created manual tier = %+v", manual)
	}

	upd := func(userID, tierID string, body map[string]any) int {
		req := withURLParams(newRequestAs(userID, http.MethodPut, "/x", body),
			"id", testWorkspaceID, "tierId", tierID)
		return testutil.Call(t, testHandler.UpdateModelTier, req).ResponseRecorder.Code
	}
	del := func(userID, tierID string) int {
		req := withURLParams(newRequestAs(userID, http.MethodDelete, "/x", nil),
			"id", testWorkspaceID, "tierId", tierID)
		return testutil.Call(t, testHandler.DeleteModelTier, req).ResponseRecorder.Code
	}
	// Admin cannot clear manual_only.
	if code := upd(adminUserID, manual.ID, map[string]any{"manual_only": false}); code != http.StatusForbidden {
		t.Fatalf("admin clear manual_only = %d, want 403", code)
	}
	// Admin cannot narrow a manual tier's roles.
	if code := upd(adminUserID, manual.ID, map[string]any{"roles": []string{}}); code != http.StatusForbidden {
		t.Fatalf("admin narrow roles = %d, want 403", code)
	}
	// Admin cannot delete a manual tier.
	if code := del(adminUserID, manual.ID); code != http.StatusForbidden {
		t.Fatalf("admin delete manual = %d, want 403", code)
	}
	// Admin CAN widen roles and edit non-protection fields.
	var widened ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequestAs(adminUserID, http.MethodPut, "/x", map[string]any{"roles": []string{"architect", "leader"}}),
		"id", testWorkspaceID, "tierId", manual.ID)).Want(http.StatusOK).JSON(&widened)
	if len(widened.Roles) != 2 {
		t.Fatalf("admin widen roles = %+v", widened)
	}
	// Owner can clear and delete.
	var cleared ModelTierResponse
	testutil.Call(t, testHandler.UpdateModelTier, withURLParams(
		newRequest(http.MethodPut, "/x", map[string]any{"manual_only": false}),
		"id", testWorkspaceID, "tierId", manual.ID)).Want(http.StatusOK).JSON(&cleared)
	if cleared.ManualOnly {
		t.Fatalf("owner clear manual_only = %+v", cleared)
	}
	if code := del(testUserID, manual.ID); code != http.StatusNoContent {
		t.Fatalf("owner delete = %d, want 204", code)
	}
}
