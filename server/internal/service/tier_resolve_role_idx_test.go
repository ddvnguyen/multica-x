package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-497: candidates order by the role-specific idx for the agent's role.

func upsertRoleIdx(t *testing.T, s *tier423Setup, tier db.ModelTier, role string, idx int32) {
	t.Helper()
	if _, err := s.qq.UpsertModelTierRole(context.Background(), db.UpsertModelTierRoleParams{
		WorkspaceID: util.MustParseUUID(s.wsID),
		TierID:      tier.ID,
		Role:        role,
		Idx:         idx,
	}); err != nil {
		t.Fatalf("upsert role idx: %v", err)
	}
}

func resolveName497(t *testing.T, s *tier423Setup, agent db.Agent) string {
	t.Helper()
	res, err := s.svc.resolveAgentTier(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return tierName423(t, s, res.tierID)
}

// TestResolve497_PerRoleOrder: global idx says A first, but the engineer's
// role list says B first — the engineer resolves B. Without role rows the
// global idx wins again.
func TestResolve497_PerRoleOrder(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 rt")
	eng := s.agentWithRole(t, "r497 eng", rt, "engineer")
	tierA := s.tier(t, "r497-a", 0, rt, []string{"engineer"}, false, nil)
	tierB := s.tier(t, "r497-b", 1, rt, []string{"engineer"}, false, nil)
	s.selectTiers(t, eng, tierA, tierB)

	if got := resolveName497(t, s, eng); got != "r497-a" {
		t.Fatalf("no role rows resolve = %s, want r497-a (global idx)", got)
	}
	upsertRoleIdx(t, s, tierA, "engineer", 5)
	upsertRoleIdx(t, s, tierB, "engineer", 1)
	if got := resolveName497(t, s, eng); got != "r497-b" {
		t.Fatalf("role-ordered resolve = %s, want r497-b", got)
	}
}

// TestResolve497_ReorderIsolation: swapping the engineer list leaves the
// architect list (and its resolution) untouched.
func TestResolve497_ReorderIsolation(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 iso rt")
	eng := s.agentWithRole(t, "r497 iso eng", rt, "engineer")
	arch := s.agentWithRole(t, "r497 iso arch", rt, "architect")
	tierA := s.tier(t, "riso-a", 0, rt, []string{"engineer", "architect"}, false, nil)
	tierB := s.tier(t, "riso-b", 1, rt, []string{"engineer", "architect"}, false, nil)
	s.selectTiers(t, eng, tierA, tierB)
	s.selectTiers(t, arch, tierA, tierB)
	upsertRoleIdx(t, s, tierA, "engineer", 0)
	upsertRoleIdx(t, s, tierB, "engineer", 1)
	upsertRoleIdx(t, s, tierA, "architect", 0)
	upsertRoleIdx(t, s, tierB, "architect", 1)
	// Reorder engineer: B first.
	upsertRoleIdx(t, s, tierB, "engineer", 0)
	upsertRoleIdx(t, s, tierA, "engineer", 1)
	if got := resolveName497(t, s, eng); got != "riso-b" {
		t.Fatalf("engineer resolve = %s, want riso-b", got)
	}
	if got := resolveName497(t, s, arch); got != "riso-a" {
		t.Fatalf("architect resolve = %s, want riso-a unchanged", got)
	}
}

// TestResolve497_TierInTwoRoles: one tier in both lists is resolvable from
// either role; legacy tiers with no role match only legacy agents.
func TestResolve497_TierInTwoRoles(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 two rt")
	eng := s.agentWithRole(t, "r497 two eng", rt, "engineer")
	arch := s.agentWithRole(t, "r497 two arch", rt, "architect")
	legacy := s.agentWithRole(t, "r497 two legacy", rt, "")
	shared := s.tier(t, "rtwo-shared", 1, rt, []string{"engineer", "architect"}, false, nil)
	open := s.tier(t, "rtwo-open", 0, rt, []string{}, false, nil)
	s.selectTiers(t, eng, shared, open)
	s.selectTiers(t, arch, shared, open)
	s.selectTiers(t, legacy, shared, open)
	upsertRoleIdx(t, s, shared, "engineer", 0)
	upsertRoleIdx(t, s, shared, "architect", 0)
	if got := resolveName497(t, s, eng); got != "rtwo-shared" {
		t.Fatalf("engineer resolve = %s, want rtwo-shared", got)
	}
	if got := resolveName497(t, s, arch); got != "rtwo-shared" {
		t.Fatalf("architect resolve = %s, want rtwo-shared", got)
	}
	// Legacy agent sees only the role-less tier (role-set tiers never match
	// a role-less agent, and vice versa).
	if got := resolveName497(t, s, legacy); got != "rtwo-open" {
		t.Fatalf("legacy resolve = %s, want rtwo-open", got)
	}
}

// TestResolve497_LegacyRoleString: an agent with the old "leader" role
// resolves by the management list.
func TestResolve497_LegacyRoleString(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 leg rt")
	leader := s.agentWithRole(t, "r497 leader", rt, "leader")
	tierA := s.tier(t, "rleg-a", 0, rt, []string{"management"}, false, nil)
	tierB := s.tier(t, "rleg-b", 1, rt, []string{"management"}, false, nil)
	s.selectTiers(t, leader, tierA, tierB)
	upsertRoleIdx(t, s, tierA, "management", 9)
	upsertRoleIdx(t, s, tierB, "management", 2)
	if got := resolveName497(t, s, leader); got != "rleg-b" {
		t.Fatalf("leader resolve = %s, want rleg-b (management order)", got)
	}
}

// TestResolve497_FallbackUsesRoleIdx: below-floor fallback breaks its
// quality-desc tie on the role idx, not the global idx.
func TestResolve497_FallbackUsesRoleIdx(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 fb rt")
	eng := s.agentWithRole(t, "r497 fb eng", rt, "engineer")
	// Floor for engineer at difficulty 1,1 is 50; both tiers measured below
	// it at equal quality, so the role idx decides the fallback.
	lo1 := s.tier(t, "rfb-1", 0, rt, []string{"engineer"}, false, i32(10))
	lo2 := s.tier(t, "rfb-2", 1, rt, []string{"engineer"}, false, i32(10))
	s.selectTiers(t, eng, lo1, lo2)
	upsertRoleIdx(t, s, lo1, "engineer", 4)
	upsertRoleIdx(t, s, lo2, "engineer", 0)
	res, err := s.svc.resolveAgentTier(context.Background(), eng)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !res.qualityFallback {
		t.Fatalf("resolve should stamp quality fallback, got %+v", res)
	}
	if got := tierName423(t, s, res.tierID); got != "rfb-2" {
		t.Fatalf("fallback resolve = %s, want rfb-2 (role idx)", got)
	}
}

// TestResolve497_CustomRole: a workspace-defined role resolves by its own
// role idx with no quality floor (Owner Q4) — even a low measured quality
// qualifies when the custom agent picks from its list.
func TestResolve497_CustomRole(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "r497 custom rt")
	qa := s.agentWithRole(t, "r497 qa", rt, "qa")
	low1 := s.tier(t, "rqa-1", 0, rt, []string{"qa"}, false, i32(10))
	low2 := s.tier(t, "rqa-2", 1, rt, []string{"qa"}, false, i32(90))
	s.selectTiers(t, qa, low1, low2)
	upsertRoleIdx(t, s, low1, "qa", 7)
	upsertRoleIdx(t, s, low2, "qa", 3)
	if got := resolveName497(t, s, qa); got != "rqa-2" {
		t.Fatalf("custom role resolve = %s, want rqa-2 (qa idx order, no floor)", got)
	}
}
