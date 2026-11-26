package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// roleRouteFixture is one Role (offline, runtime-less) with a single
// role-keyed tier carrying a single member. roleKey is unique per fixture so
// concurrent tests never share a tier list.
type roleRouteFixture struct {
	roleKey string
	roleID  string
	tierID  string
	rtID    string
}

// setupRoleRoute creates a Role with subtype role, status offline and no
// runtime, plus one online runtime, one tier in the Role's keyed list and one
// enabled member on that tier. tierCols/memberCols/runtimeCols override the
// insert columns for the cooldown/manual_only/capacity cases.
func setupRoleRoute(t *testing.T, tierCols, memberCols, runtimeCols testutil.Cols) roleRouteFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	suffix := time.Now().Format("150405000000000")
	roleKey := "rr" + suffix

	roleID := dbfx.Agent(t, "rr-role-"+suffix, "", testutil.Cols{
		"subtype":         "role",
		"tier_role":       roleKey,
		"status":          "offline",
		"use_tiers":       false,
		"permission_mode": "private",
	})
	rtID := dbfx.Runtime(t, "rr-rt-"+suffix, runtimeCols)
	tierCols = mergeTestCols(testutil.Cols{
		"workspace_id":   testWorkspaceID,
		"name":           "rr-tier-" + suffix,
		"idx":            0,
		"provider":       "rrp",
		"model":          "rrm",
		"provider_slots": 2,
		"enabled":        true,
		"manual_only":    false,
		"auto_pick":      true,
		"roles":          testutil.Raw("ARRAY['" + roleKey + "']::text[]"),
	}, tierCols)
	tierID := dbfx.Insert(t, "model_tier", tierCols)
	dbfx.Exec(t, `INSERT INTO model_tier_role (workspace_id, tier_id, role, idx) VALUES ($1, $2, $3, 0)`,
		testWorkspaceID, tierID, roleKey)
	memberCols = mergeTestCols(testutil.Cols{
		"tier_id":        tierID,
		"position":       0,
		"provider":       "rrp",
		"model":          "rrm",
		"runtime_id":     rtID,
		"provider_slots": 2,
		"enabled":        true,
	}, memberCols)
	dbfx.Insert(t, "model_tier_member", memberCols)
	return roleRouteFixture{roleKey: roleKey, roleID: roleID, tierID: tierID, rtID: rtID}
}

func mergeTestCols(base testutil.Cols, over testutil.Cols) testutil.Cols {
	out := testutil.Cols{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func getRoleRoute(t *testing.T, userID, roleID string) (RoleRouteResponse, int) {
	t.Helper()
	rec := testutil.Call(t, testHandler.GetAgentRoleRoute,
		withURLParam(newRequestAs(userID, http.MethodGet, "/api/agents/"+roleID+"/role-route", nil), "id", roleID)).ResponseRecorder
	if rec.Code != http.StatusOK {
		return RoleRouteResponse{}, rec.Code
	}
	var out RoleRouteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode role route: %v", err)
	}
	return out, rec.Code
}

// sweepUsable mirrors the orch-sweep role_route_ready predicate on the
// fields it consumes: flag on, one eligible tier, one online fresh member
// with a free slot.
func sweepUsable(t *testing.T, route RoleRouteResponse) {
	t.Helper()
	if !route.FlagOn {
		t.Fatal("flag_on = false, want true")
	}
	if len(route.Tiers) != 1 {
		t.Fatalf("tiers = %d, want 1", len(route.Tiers))
	}
	tier := route.Tiers[0]
	if !tier.Enabled || tier.ManualOnly || tier.CooldownUntil != nil {
		t.Fatalf("tier not eligible: %+v", tier)
	}
	if len(tier.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(tier.Members))
	}
	m := tier.Members[0]
	if m.RuntimeStatus != "online" {
		t.Fatalf("runtime_status = %q, want online", m.RuntimeStatus)
	}
	if m.RuntimeLastSeenAt == nil {
		t.Fatal("runtime_last_seen_at is null")
	}
	seen, err := time.Parse(time.RFC3339, *m.RuntimeLastSeenAt)
	if err != nil {
		t.Fatalf("parse runtime_last_seen_at: %v", err)
	}
	if age := time.Since(seen); age < 0 || age > 15*time.Minute {
		t.Fatalf("runtime_last_seen_at age = %v, want within 15m", age)
	}
	if m.FreeSlots < 1 {
		t.Fatalf("free_slots = %d, want >= 1", m.FreeSlots)
	}
}

// TestGetAgentRoleRouteUsable pins the G1 contract: a Role that is offline
// and runtime-less still reports a usable route when an online tier member
// has a free slot. The Role's own status/runtime_bound must not matter.
func TestGetAgentRoleRouteUsable(t *testing.T) {
	fx := setupRoleRoute(t, nil, nil, nil)

	role, err := testHandler.Queries.GetAgent(context.Background(), parseUUID(fx.roleID))
	if err != nil {
		t.Fatalf("load role: %v", err)
	}
	if role.Status == "online" || role.RuntimeID.Valid {
		t.Fatalf("fixture role must be offline/unbound, got status=%q runtime valid=%v", role.Status, role.RuntimeID.Valid)
	}

	route, code := getRoleRoute(t, testUserID, fx.roleID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	sweepUsable(t, route)
	if got := route.Tiers[0].Members[0].FreeSlots; got != 2 {
		t.Fatalf("free_slots = %d, want 2 (no active runs)", got)
	}
}

// TestGetAgentRoleRouteCooldown pins that a cooling tier keeps its
// cooldown_until timestamp so the probe skips it (no_eligible_tier when it is
// the only tier).
func TestGetAgentRoleRouteCooldown(t *testing.T) {
	fx := setupRoleRoute(t, testutil.Cols{
		"cooldown_until": testutil.Raw("now() + interval '1 hour'"),
	}, nil, nil)

	route, code := getRoleRoute(t, testUserID, fx.roleID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(route.Tiers) != 1 {
		t.Fatalf("tiers = %d, want 1", len(route.Tiers))
	}
	cd := route.Tiers[0].CooldownUntil
	if cd == nil {
		t.Fatal("cooldown_until is null, want future timestamp")
	}
	until, err := time.Parse(time.RFC3339, *cd)
	if err != nil {
		t.Fatalf("parse cooldown_until: %v", err)
	}
	if !until.After(time.Now()) {
		t.Fatalf("cooldown_until = %v, want future", until)
	}
}

// TestGetAgentRoleRouteManualOnly pins that a manual-only tier is reported
// verbatim so the probe (not this endpoint) excludes it.
func TestGetAgentRoleRouteManualOnly(t *testing.T) {
	fx := setupRoleRoute(t, testutil.Cols{"manual_only": true}, nil, nil)

	route, code := getRoleRoute(t, testUserID, fx.roleID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(route.Tiers) != 1 || !route.Tiers[0].ManualOnly {
		t.Fatalf("manual_only not reported: %+v", route.Tiers)
	}
}

// TestGetAgentRoleRouteNoCapacity pins free_slots = 0 when every provider
// slot is held by an active run: the probe must report no_capacity.
func TestGetAgentRoleRouteNoCapacity(t *testing.T) {
	fx := setupRoleRoute(t, testutil.Cols{"provider_slots": 1},
		testutil.Cols{"provider_slots": 1}, nil)
	dbfx.Task(t, fx.roleID, testutil.Cols{"status": "running", "runtime_id": fx.rtID})

	route, code := getRoleRoute(t, testUserID, fx.roleID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if len(route.Tiers) != 1 || len(route.Tiers[0].Members) != 1 {
		t.Fatalf("shape changed: %+v", route.Tiers)
	}
	if got := route.Tiers[0].Members[0].FreeSlots; got != 0 {
		t.Fatalf("free_slots = %d, want 0 (slot held by running task)", got)
	}
}

// TestGetAgentRoleRouteUnknown pins the fail-closed contract: an unknown
// Role is 404 so the CLI exits non-zero.
func TestGetAgentRoleRouteUnknown(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	_, code := getRoleRoute(t, testUserID, "00000000-0000-0000-0000-000000000000")
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

// TestGetAgentRoleRouteNonRole pins that a regular (non-Role) agent id is
// 404: this endpoint never describes an implementing agent's own route.
func TestGetAgentRoleRouteNonRole(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	plain := dbfx.Agent(t, "rr-plain-"+time.Now().Format("150405000000000"), "", nil)
	_, code := getRoleRoute(t, testUserID, plain)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

// TestGetAgentRoleRouteFlagOff pins that the workspace flag is reported
// honestly: flag off still returns the tier list with flag_on=false.
func TestGetAgentRoleRouteFlagOff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	suffix := time.Now().Format("150405000000000")
	roleKey := "rrf" + suffix
	roleID := dbfx.Agent(t, "rr-role-"+suffix, "", testutil.Cols{
		"subtype": "role", "tier_role": roleKey, "status": "offline",
		"use_tiers": false, "permission_mode": "private",
	})
	tierID := dbfx.Insert(t, "model_tier", testutil.Cols{
		"workspace_id": testWorkspaceID, "name": "rr-tier-" + suffix, "idx": 0,
		"provider": "rrp", "model": "rrm", "provider_slots": 1,
		"enabled": true, "manual_only": false, "auto_pick": true,
		"roles": testutil.Raw("ARRAY['" + roleKey + "']::text[]"),
	})
	dbfx.Exec(t, `INSERT INTO model_tier_role (workspace_id, tier_id, role, idx) VALUES ($1, $2, $3, 0)`,
		testWorkspaceID, tierID, roleKey)

	route, code := getRoleRoute(t, testUserID, roleID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if route.FlagOn {
		t.Fatal("flag_on = true, want false")
	}
	if len(route.Tiers) != 1 {
		t.Fatalf("tiers = %d, want 1 (flag off hides nothing)", len(route.Tiers))
	}
}
