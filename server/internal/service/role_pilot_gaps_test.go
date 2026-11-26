package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// roleForPilot creates the Role shape a production pilot runs (M-579/M-589):
// a model-free Role row with no runtime and no model, use_tiers on, whose
// tiers come from the role-keyed M-497 index rather than agent_tier.
func roleForPilot(t *testing.T, s *tier423Setup, name, roleKey string) db.Agent {
	t.Helper()
	id := util.MustParseUUID(s.fx.Agent(t, name, "", testutil.Cols{
		"subtype":   "role",
		"tier_role": roleKey,
		"use_tiers": true,
	}))
	role, err := s.qq.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("load role %s: %v", name, err)
	}
	return role
}

// F2 (M-589): a valid explicit `#tier` on a use_tiers Role must resolve from
// the Role's role-keyed tier list. LookupExplicitTierAs used to validate every
// use_tiers agent against agent_tier, which a Role never populates, so the
// pick failed as errUnknownTier ("unknown or unavailable tier") even though
// the tier is in the Role's list.
func TestRoleExplicitTierNameResolvesFromRoleIndex(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m589 f2 rt")
	pick := s.tier(t, "m589-f2-tier", 0, rt, []string{"engineer"}, false, i32(60))
	other := s.tier(t, "m589-f2-management-tier", 1, rt, []string{"management"}, false, i32(70))
	role := roleForPilot(t, s, "m589 f2 role", "engineer")
	requester := util.MustParseUUID(s.fx.UserID)

	got, err := s.svc.LookupExplicitTierAs(ctx, role, role.WorkspaceID, pick.Name, requester)
	if err != nil {
		t.Fatalf("explicit #%s on a use_tiers Role: %v, want the role-keyed tier", pick.Name, err)
	}
	if util.UUIDToString(got.ID) != util.UUIDToString(pick.ID) {
		t.Fatalf("resolved tier = %s, want %s", util.UUIDToString(got.ID), util.UUIDToString(pick.ID))
	}

	// The fix must not open the Role's list: a tier carried only by another
	// role stays errUnknownTier, exactly like the by-id path (roleExplicitTier).
	if _, err := s.svc.LookupExplicitTierAs(ctx, role, role.WorkspaceID, other.Name, requester); !errors.Is(err, errUnknownTier) {
		t.Fatalf("tier outside the Role's list err = %v, want errUnknownTier", err)
	}
}

// F3c (M-589): a Role auto-pick on an issue with no important_level/complexity
// uses the 1/1 defaults and must not error. Engineer's floor is 50 at 1/1, so a
// quality-50 tier is a first-choice pick (not a below-floor fallback) — any
// larger default would flip that stamp to a fallback.
func TestRoleAutoPickWithoutDifficultyFields(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m589 f3c rt")
	atFloor := s.tier(t, "m589-f3c-at-floor", 0, rt, []string{"engineer"}, false, i32(50))
	role := roleForPilot(t, s, "m589 f3c role", "engineer")
	issueID := util.MustParseUUID(s.fx.Issue(t, "m589 f3c issue", testutil.Cols{}))

	for _, tc := range []struct {
		name       string
		issueID    pgtype.UUID
		properties []byte
	}{
		{name: "no issue context", issueID: pgtype.UUID{}, properties: nil},
		{name: "issue without properties", issueID: issueID, properties: nil},
		{name: "issue with an empty property bag", issueID: issueID, properties: []byte(`{}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := s.svc.resolveRoleTier(ctx, role, "engineer", tc.issueID, tc.properties)
			if err != nil {
				t.Fatalf("resolveRoleTier without difficulty fields: %v", err)
			}
			if util.UUIDToString(res.tierID) != util.UUIDToString(atFloor.ID) {
				t.Fatalf("resolved tier = %s, want %s", util.UUIDToString(res.tierID), util.UUIDToString(atFloor.ID))
			}
			if res.qualityFallback {
				t.Fatal("quality-50 tier is the engineer floor at 1/1, so the pick must not be a below-floor fallback")
			}
		})
	}
}
