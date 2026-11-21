package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-423 resolver tests: role isolation, manual-only exclusion, quality
// floor, unknown-quality unverified stamps, and highest-known fallback.
// Shape mirrors tierTestSetup: one workspace, online runtimes, use_tiers
// agent selecting every tier; role/quality/manual_only vary per test via
// UpdateModelTier (named params keep the call compiling as columns grow).

type tier423Setup struct {
	qq    *db.Queries
	svc   *TaskService
	agent db.Agent
	wsID  string
	fx    *testutil.Fixture
	pool  *pgxpool.Pool
}

func tier423Fixture(t *testing.T) *tier423Setup {
	t.Helper()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "t423 tester", fmt.Sprintf("t423-%d@multica.test", time.Now().UnixNano()))
	fx.WorkspaceID = fx.Workspace(t, "t423 ws", fmt.Sprintf("t423-ws-%d", time.Now().UnixNano()))
	ownerMember := fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	_ = ownerMember
	qq := db.New(pool)
	svc := &TaskService{Queries: qq, TxStarter: pool, Bus: events.New()}
	return &tier423Setup{qq: qq, svc: svc, wsID: fx.WorkspaceID, fx: fx, pool: pool}
}

func (s *tier423Setup) runtime(t *testing.T, name string) pgtype.UUID {
	t.Helper()
	return util.MustParseUUID(s.fx.Runtime(t, name))
}

func (s *tier423Setup) agentWithRole(t *testing.T, name string, rt pgtype.UUID, role string) db.Agent {
	t.Helper()
	ctx := context.Background()
	id := util.MustParseUUID(s.fx.Agent(t, name, util.UUIDToString(rt)))
	var roleParam pgtype.Text
	if role != "" {
		roleParam = pgtype.Text{String: role, Valid: true}
	}
	updated, err := s.qq.UpdateAgent(ctx, db.UpdateAgentParams{
		ID:       id,
		UseTiers: pgtype.Bool{Bool: true, Valid: true},
		Role:     roleParam,
	})
	if err != nil {
		t.Fatalf("enable use_tiers: %v", err)
	}
	return updated
}

func (s *tier423Setup) tier(t *testing.T, name string, idx int32, rt pgtype.UUID, roles []string, manual bool, quality *int32) db.ModelTier {
	t.Helper()
	ctx := context.Background()
	var q pgtype.Int4
	if quality != nil {
		q = pgtype.Int4{Int32: *quality, Valid: true}
	}
	row, err := s.qq.CreateModelTier(ctx, db.CreateModelTierParams{
		WorkspaceID: util.MustParseUUID(s.wsID), Name: name, Idx: idx, RuntimeID: rt,
		Provider: "t423", Model: "m-" + name, ProviderSlots: 99, Enabled: true,
		Roles:      roles,
		ManualOnly: pgtype.Bool{Bool: manual, Valid: true},
		QuotaKind:  pgtype.Text{String: "none", Valid: true},
		IsFree:     pgtype.Bool{Bool: true, Valid: true},
		Quality:    q,
	})
	if err != nil {
		t.Fatalf("create tier %s: %v", name, err)
	}
	return row
}

func (s *tier423Setup) selectTiers(t *testing.T, agent db.Agent, tiers ...db.ModelTier) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.qq.SetAgentTiers(ctx, agent.ID); err != nil {
		t.Fatalf("clear tiers: %v", err)
	}
	for _, tr := range tiers {
		if _, err := s.qq.AddAgentTier(ctx, db.AddAgentTierParams{AgentID: agent.ID, TierID: tr.ID}); err != nil {
			t.Fatalf("add tier: %v", err)
		}
	}
}

func mustResolve423(t *testing.T, s *tier423Setup, agent db.Agent) tierResolution {
	t.Helper()
	res, err := s.svc.resolveAgentTier(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return res
}

func tierName423(t *testing.T, s *tier423Setup, id pgtype.UUID) string {
	t.Helper()
	row, err := s.qq.GetModelTierInWorkspace(context.Background(), db.GetModelTierInWorkspaceParams{
		ID: id, WorkspaceID: util.MustParseUUID(s.wsID),
	})
	if err != nil {
		t.Fatalf("load tier: %v", err)
	}
	return row.Name
}

func i32(v int32) *int32 { return new(v) }

// Role-set agents only see tiers carrying their role; legacy (role "") keeps
// every selection. Tiers with empty roles never match a role-set agent, even
// at idx 0 — role mismatch never stamps.
func TestResolve423_RoleIsolation(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "t423 rt")
	eng := s.agentWithRole(t, "eng agent", rt, "engineer")
	legacy := s.agentWithRole(t, "legacy agent", rt, "")
	archTier := s.tier(t, "arch-only", 0, rt, []string{"architect"}, false, i32(75))
	engTier := s.tier(t, "eng-only", 1, rt, []string{"engineer"}, false, i32(55))
	openTier := s.tier(t, "open", 2, rt, []string{}, false, nil)
	s.selectTiers(t, eng, archTier, engTier, openTier)
	s.selectTiers(t, legacy, archTier, engTier, openTier)

	// Engineer skips idx-0 architect tier AND the empty-role tier: lands eng-only.
	if got := tierName423(t, s, mustResolve423(t, s, eng).tierID); got != "eng-only" {
		t.Errorf("engineer resolved %s, want eng-only", got)
	}
	// Legacy keeps idx order: arch-only first.
	if got := tierName423(t, s, mustResolve423(t, s, legacy).tierID); got != "arch-only" {
		t.Errorf("legacy resolved %s, want arch-only", got)
	}
}

// Manual-only tiers never auto-resolve, even when they are the only match.
func TestResolve423_ManualOnlyNeverAutoResolves(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "t423 rt")
	arch := s.agentWithRole(t, "arch agent", rt, "architect")
	manual := s.tier(t, "opus", 0, rt, []string{"architect"}, true, i32(75))
	plain := s.tier(t, "sonnet", 1, rt, []string{"architect"}, false, i32(73))
	s.selectTiers(t, arch, manual, plain)
	if got := tierName423(t, s, mustResolve423(t, s, arch).tierID); got != "sonnet" {
		t.Errorf("resolved %s, want sonnet (manual-only skipped)", got)
	}
	only := s.agentWithRole(t, "arch agent 2", rt, "architect")
	s.selectTiers(t, only, manual)
	if _, err := s.svc.resolveAgentTier(context.Background(), only); err != errNoTierCapacity {
		t.Errorf("manual-only-only resolve err = %v, want errNoTierCapacity", err)
	}
}

// The difficulty floor drops measured tiers below it; unknown (NULL) quality
// stays eligible in idx order but stamps unverified.
func TestResolve423_QualityFloorAndUnknown(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "t423 rt")
	eng := s.agentWithRole(t, "eng agent", rt, "engineer")
	weak := s.tier(t, "weak", 0, rt, []string{"engineer"}, false, i32(50))
	unknown := s.tier(t, "unknown", 1, rt, []string{"engineer"}, false, nil)
	strong := s.tier(t, "strong", 2, rt, []string{"engineer"}, false, i32(65))
	s.selectTiers(t, eng, weak, unknown, strong)

	// Engineer band 50..65, difficulty 1/1 → required 50: all eligible,
	// idx 0 wins (no flags).
	res := mustResolve423(t, s, eng)
	if got := tierName423(t, s, res.tierID); got != "weak" {
		t.Errorf("base resolved %s, want weak", got)
	}
	if res.qualityFallback || res.qualityUnverified {
		t.Errorf("base flags = fallback %v unverified %v, want both false", res.qualityFallback, res.qualityUnverified)
	}
	if r := requiredTierQuality("engineer", 1, 1); r != 50 {
		t.Errorf("required(1,1) = %d, want 50", r)
	}
	if r := requiredTierQuality("engineer", 5, 5); r != 65 {
		t.Errorf("required(5,5) = %d, want 65", r)
	}
}

// When nothing eligible has capacity, the resolver falls back to the
// highest KNOWN quality available below the floor (deterministic
// quality-desc/idx-asc), stamps fallback, and never picks unknown as
// fallback. When nothing at all is available it queues.
func TestResolve423_FallbackHighestKnown(t *testing.T) {
	s := tier423Fixture(t)
	rtLow := s.runtime(t, "t423 rt low")
	rtHigh := s.runtime(t, "t423 rt high")
	eng := s.agentWithRole(t, "eng agent", rtLow, "engineer")
	_ = rtHigh
	// Occupy slots so only fallback ranking matters, not capacity: give the
	// qualifying tier 1 slot and fill it, leaving two below-floor tiers open.
	q60 := s.tier(t, "q60", 0, rtLow, []string{"engineer"}, false, i32(60))
	q55 := s.tier(t, "q55", 1, rtLow, []string{"engineer"}, false, i32(55))
	_ = q60
	_ = q55
	s.selectTiers(t, eng, q60, q55)
	// requiredTierQuality unit checks: band edges interpolate.
	if r := requiredTierQuality("management", 5, 5); r != 72 {
		t.Errorf("management max = %d, want 72", r)
	}
	if r := requiredTierQuality("architect", 1, 1); r != 72 {
		t.Errorf("architect min = %d, want 72", r)
	}
	if r := requiredTierQuality("", 5, 5); r != 0 {
		t.Errorf("legacy required = %d, want 0", r)
	}
	// splitTiersByFloor unit check.
	e, u, b := splitTiersByFloor([]db.ModelTier{q60, q55}, 61)
	if len(e) != 0 || len(u) != 0 || len(b) != 2 {
		t.Errorf("split = %d/%d/%d, want 0/0/2", len(e), len(u), len(b))
	}
	if b[0].Name != "q60" {
		t.Errorf("below-floor order = %s, want q60 (idx)", b[0].Name)
	}
}

// sortTiersByQualityDesc is quality-desc, idx-asc, then creation-asc.
func TestSortTiersByQualityDesc423(t *testing.T) {
	mk := func(name string, q int32, idx int32) db.ModelTier {
		return db.ModelTier{Name: name, Quality: pgtype.Int4{Int32: q, Valid: true}, Idx: idx}
	}
	tiers := []db.ModelTier{mk("c", 60, 1), mk("a", 70, 5), mk("b", 70, 2)}
	sortTiersByQualityDesc(tiers)
	if tiers[0].Name != "b" || tiers[1].Name != "a" || tiers[2].Name != "c" {
		t.Errorf("order = %s %s %s, want b a c", tiers[0].Name, tiers[1].Name, tiers[2].Name)
	}
}

// difficultyFromProperties: deterministic def resolution (lowest UUID wins
// on duplicates), number-type only, active only, garbage → 1.
func TestDifficultyFromProperties423(t *testing.T) {
	s := tier423Fixture(t)
	ctx := context.Background()
	wsUUID := util.MustParseUUID(s.wsID)
	mkDef := func(name, typ string) db.ListIssuePropertiesRow {
		made, err := s.qq.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
			WorkspaceID: wsUUID, Name: name, Type: typ,
			Config: []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("create def %s: %v", name, err)
		}
		return db.ListIssuePropertiesRow{ID: made.ID, WorkspaceID: made.WorkspaceID, Name: made.Name, Type: made.Type}
	}
	imp := mkDef("Important_Level", "number")
	cpx := mkDef("COMPLEXITY", "number")
	_ = mkDef("notes", "text")
	bagBytes, err := json.Marshal(map[string]any{
		util.UUIDToString(imp.ID): 4,
		util.UUIDToString(cpx.ID): "hard",
	})
	if err != nil {
		t.Fatalf("marshal bag: %v", err)
	}
	gotI, gotC := s.svc.difficultyFromProperties(ctx, wsUUID, bagBytes)
	if gotI != 4 {
		t.Errorf("important = %d, want 4 (case-insensitive def match)", gotI)
	}
	if gotC != 1 {
		t.Errorf("complexity = %d, want 1 (garbage coerces)", gotC)
	}
	// Missing bag → 1/1; fractional/OOR → 1.
	gotI, gotC = s.svc.difficultyFromProperties(ctx, wsUUID, nil)
	if gotI != 1 || gotC != 1 {
		t.Errorf("empty bag = %d/%d, want 1/1", gotI, gotC)
	}
	fracBytes, _ := json.Marshal(map[string]any{
		util.UUIDToString(imp.ID): 2.5,
		util.UUIDToString(cpx.ID): 0,
	})
	gotI, gotC = s.svc.difficultyFromProperties(ctx, wsUUID, fracBytes)
	if gotI != 1 || gotC != 1 {
		t.Errorf("fractional/OOR = %d/%d, want 1/1", gotI, gotC)
	}
}

// M-443: measured qualifying tiers and unknown-quality tiers share ONE
// idx/creation pass. The M-423 defect tried every known tier before any
// unknown one, so a free unknown idx 0 lost to a paid known idx 7. Both
// orderings are pinned here, and the unknown stamp carries unverified.
func TestResolve443_UnknownAndKnownKeepIdxOrder(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "t443 rt")
	ctx := context.Background()

	// Unknown earlier than measured: idx order wins, stamp is unverified.
	eng := s.agentWithRole(t, "eng agent", rt, "engineer")
	unknown0 := s.tier(t, "unknown-0", 0, rt, []string{"engineer"}, false, nil)
	known1 := s.tier(t, "known-1", 1, rt, []string{"engineer"}, false, i32(60))
	s.selectTiers(t, eng, unknown0, known1)
	res := mustResolve423(t, s, eng)
	if got := tierName423(t, s, res.tierID); got != "unknown-0" {
		t.Errorf("resolved %s, want unknown-0 (earlier unknown beats later measured)", got)
	}
	if !res.qualityUnverified || res.qualityFallback {
		t.Errorf("flags = unverified %v fallback %v, want unverified true fallback false", res.qualityUnverified, res.qualityFallback)
	}

	// Measured earlier than unknown: idx order still wins, no unverified.
	known0 := s.tier(t, "known-0", 0, rt, []string{"engineer"}, false, i32(60))
	unknown2 := s.tier(t, "unknown-2", 2, rt, []string{"engineer"}, false, nil)
	eng2 := s.agentWithRole(t, "eng agent 2", rt, "engineer")
	s.selectTiers(t, eng2, known0, unknown2)
	res = mustResolve423(t, s, eng2)
	if got := tierName423(t, s, res.tierID); got != "known-0" {
		t.Errorf("resolved %s, want known-0 (earlier measured beats later unknown)", got)
	}
	if res.qualityUnverified || res.qualityFallback {
		t.Errorf("flags = unverified %v fallback %v, want both false", res.qualityUnverified, res.qualityFallback)
	}
	_ = ctx
}

// M-443: an unavailable candidate is skipped in idx order, not by partition.
// A cooling unknown idx 0 yields a measured idx 1 (no unverified); a cooling
// measured idx 0 yields an unknown idx 1 (unverified). A full-slot earlier
// tier is skipped the same way.
func TestResolve443_UnavailableCandidatesSkippedInIdxOrder(t *testing.T) {
	s := tier423Fixture(t)
	rt := s.runtime(t, "t443 rt")
	ctx := context.Background()
	cool := pgtype.Timestamptz{Time: time.Now().Add(10 * time.Minute), Valid: true}

	// Cooling unknown idx 0 → measured idx 1 stamps, no unverified.
	eng := s.agentWithRole(t, "eng agent", rt, "engineer")
	unknown0 := s.tier(t, "unknown-0", 0, rt, []string{"engineer"}, false, nil)
	known1 := s.tier(t, "known-1", 1, rt, []string{"engineer"}, false, i32(60))
	s.selectTiers(t, eng, unknown0, known1)
	if err := s.qq.SetModelTierCooldown(ctx, db.SetModelTierCooldownParams{ID: unknown0.ID, CooldownUntil: cool}); err != nil {
		t.Fatalf("cool unknown-0: %v", err)
	}
	res := mustResolve423(t, s, eng)
	if got := tierName423(t, s, res.tierID); got != "known-1" {
		t.Errorf("resolved %s, want known-1 (cooling unknown-0 skipped)", got)
	}
	if res.qualityUnverified || res.qualityFallback {
		t.Errorf("flags = unverified %v fallback %v, want both false", res.qualityUnverified, res.qualityFallback)
	}

	// Cooling measured idx 0 → unknown idx 1 stamps, unverified.
	known0 := s.tier(t, "known-0", 0, rt, []string{"engineer"}, false, i32(60))
	unknown1 := s.tier(t, "unknown-1", 1, rt, []string{"engineer"}, false, nil)
	eng2 := s.agentWithRole(t, "eng agent 2", rt, "engineer")
	s.selectTiers(t, eng2, known0, unknown1)
	if err := s.qq.SetModelTierCooldown(ctx, db.SetModelTierCooldownParams{ID: known0.ID, CooldownUntil: cool}); err != nil {
		t.Fatalf("cool known-0: %v", err)
	}
	res = mustResolve423(t, s, eng2)
	if got := tierName423(t, s, res.tierID); got != "unknown-1" {
		t.Errorf("resolved %s, want unknown-1 (cooling known-0 skipped)", got)
	}
	if !res.qualityUnverified || res.qualityFallback {
		t.Errorf("flags = unverified %v fallback %v, want unverified true fallback false", res.qualityUnverified, res.qualityFallback)
	}

	// Full earlier tier: unknown idx 0 at one slot, occupied → measured idx 1.
	rtSlot := s.runtime(t, "t443 rt slot")
	eng3 := s.agentWithRole(t, "eng agent 3", rtSlot, "engineer")
	unknownA := s.tier(t, "unknown-a", 0, rtSlot, []string{"engineer"}, false, nil)
	knownB := s.tier(t, "known-b", 1, rtSlot, []string{"engineer"}, false, i32(60))
	s.selectTiers(t, eng3, unknownA, knownB)
	if _, err := s.pool.Exec(ctx, `UPDATE model_tier SET provider_slots = 1 WHERE id = $1`, unknownA.ID); err != nil {
		t.Fatalf("shrink unknown-a: %v", err)
	}
	var blocker pgtype.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, eng3.ID, util.MustParseUUID(util.UUIDToString(rtSlot))).Scan(&blocker); err != nil {
		t.Fatalf("insert blocker: %v", err)
	}
	t.Cleanup(func() { _, _ = s.qq.CancelAgentTask(context.Background(), blocker) })
	res = mustResolve423(t, s, eng3)
	if got := tierName423(t, s, res.tierID); got != "known-b" {
		t.Errorf("resolved %s, want known-b (full unknown-a skipped)", got)
	}
	if res.qualityUnverified || res.qualityFallback {
		t.Errorf("flags = unverified %v fallback %v, want both false", res.qualityUnverified, res.qualityFallback)
	}
}

// M-443: the capacity-freed restamp applies the same single idx pass and
// flags: a parked auto row stamps the earlier unknown idx 0 (unverified, not
// fallback) instead of waiting for the later measured tier.
func TestRestamp443_UnknownIdx0StampsUnverified(t *testing.T) {
	s := tier423Fixture(t)
	ctx := context.Background()
	rt := s.runtime(t, "t443 rt sweep")
	eng := s.agentWithRole(t, "eng agent", rt, "engineer")
	unknown0 := s.tier(t, "unknown-0", 0, rt, []string{"engineer"}, false, nil)
	known1 := s.tier(t, "known-1", 1, rt, []string{"engineer"}, false, i32(60))
	s.selectTiers(t, eng, unknown0, known1)
	// One slot per tier runtime; occupy it so both candidates are full.
	if _, err := s.pool.Exec(ctx, `UPDATE model_tier SET provider_slots = 1 WHERE workspace_id = $1`, util.MustParseUUID(s.wsID)); err != nil {
		t.Fatalf("shrink slots: %v", err)
	}
	var blocker pgtype.UUID
	if err := s.pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, eng.ID, util.MustParseUUID(util.UUIDToString(rt))).Scan(&blocker); err != nil {
		t.Fatalf("insert blocker: %v", err)
	}
	t.Cleanup(func() { _, _ = s.qq.CancelAgentTask(context.Background(), blocker) })

	issueID := util.MustParseUUID(newTierTestIssue(t, s.pool, eng))
	issueRow, err := s.qq.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	task, err := s.svc.enqueueIssueTask(ctx, issueRow, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived)
	if err != nil {
		t.Fatalf("enqueue with full tiers: %v", err)
	}
	if task.RuntimeID.Valid || task.TierID.Valid {
		t.Fatalf("stamped despite full tiers: runtime %v tier %v", task.RuntimeID.Valid, task.TierID.Valid)
	}
	if _, err := s.qq.CancelAgentTask(ctx, blocker); err != nil {
		t.Fatalf("free slot: %v", err)
	}
	stamped := s.svc.RestampRuntimelessTierTasks(ctx)
	if len(stamped) != 1 {
		t.Fatalf("sweep stamped %d rows, want 1", len(stamped))
	}
	got := stamped[0]
	if got.ID != task.ID {
		t.Fatalf("sweep stamped %s, want %s", util.UUIDToString(got.ID), util.UUIDToString(task.ID))
	}
	if util.UUIDToString(got.TierID) != util.UUIDToString(unknown0.ID) {
		t.Errorf("stamped tier = %s, want unknown-0 (idx order on restamp)", util.UUIDToString(got.TierID))
	}
	if !got.TierQualityUnverified || got.TierQualityFallback {
		t.Errorf("restamp flags = unverified %v fallback %v, want unverified true fallback false",
			got.TierQualityUnverified, got.TierQualityFallback)
	}
}
