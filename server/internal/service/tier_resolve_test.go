package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tierTestSetup builds a workspace with two online runtimes and two tiers
// (idx 0 on rtA with `slotsA` provider slots, idx 1 on rtB with 99 slots),
// plus a use_tiers agent selecting both. Returns the ids the resolver tests
// need. Claim-fence tests use the same shape through the handler package.
func tierTestSetup(t *testing.T, slotsA int32) (q *db.Queries, agent db.Agent, rtA, rtB, tierA, tierB string) {
	t.Helper()
	ctx := context.Background()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "tier tester", fmt.Sprintf("tier-tester-%d@multica.test", time.Now().UnixNano()))
	fx.WorkspaceID = fx.Workspace(t, "tier ws", fmt.Sprintf("tier-ws-%d", time.Now().UnixNano()))
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	rtA = fx.Runtime(t, "tier rt A")
	rtB = fx.Runtime(t, "tier rt B")
	wsUUID := util.MustParseUUID(fx.WorkspaceID)
	rtAUUID := util.MustParseUUID(rtA)
	rtBUUID := util.MustParseUUID(rtB)
	qq := db.New(pool)
	tA, err := qq.CreateModelTier(ctx, db.CreateModelTierParams{
		WorkspaceID: wsUUID, Name: "tier-a", Idx: 0, RuntimeID: rtAUUID,
		Provider: "codebuddy", Model: "model-a", ProviderSlots: slotsA, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create tier A: %v", err)
	}
	tB, err := qq.CreateModelTier(ctx, db.CreateModelTierParams{
		WorkspaceID: wsUUID, Name: "tier-b", Idx: 1, RuntimeID: rtBUUID,
		Provider: "codebuddy", Model: "model-b", ProviderSlots: 99, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create tier B: %v", err)
	}
	tierA, tierB = util.UUIDToString(tA.ID), util.UUIDToString(tB.ID)
	agentID := fx.Agent(t, "tier agent", rtA)
	agentUUID := util.MustParseUUID(agentID)
	ag, err := qq.GetAgent(ctx, agentUUID)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	if _, err := qq.SetAgentTiers(ctx, agentUUID); err != nil {
		t.Fatalf("clear agent tiers: %v", err)
	}
	for _, tid := range []pgtype.UUID{tA.ID, tB.ID} {
		if _, err := qq.AddAgentTier(ctx, db.AddAgentTierParams{AgentID: agentUUID, TierID: tid}); err != nil {
			t.Fatalf("add agent tier: %v", err)
		}
	}
	// Flip use_tiers on with the UpdateAgent query path (COALESCE keeps it sticky).
	updated, err := qq.UpdateAgent(ctx, db.UpdateAgentParams{
		ID:       agentUUID,
		UseTiers: pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil {
		t.Fatalf("enable use_tiers: %v", err)
	}
	_ = ag
	svc := newTierTestService(pool, qq)
	_ = svc
	return qq, updated, rtA, rtB, tierA, tierB
}

func newTierTestService(pool *pgxpool.Pool, qq *db.Queries) *TaskService {
	return &TaskService{Queries: qq, TxStarter: pool, Bus: events.New()}
}

// TestResolveAgentTier_PrefersFirstWithCapacity pins the resolver order: idx
// 0 wins while it has a free provider slot; the task lands on its runtime
// with the tier stamp.
func TestResolveAgentTier_PrefersFirstWithCapacity(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()
	qq, agent, rtA, _, tierA, _ := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := util.UUIDToString(res.runtimeID); got != rtA {
		t.Errorf("runtime = %s, want tier-A runtime %s", got, rtA)
	}
	if got := util.UUIDToString(res.tierID); got != tierA {
		t.Errorf("tier = %s, want %s", got, tierA)
	}
	if !res.modelOverride.Valid || res.modelOverride.String != "model-a" {
		t.Errorf("modelOverride = %+v, want model-a", res.modelOverride)
	}
	_ = pool
}

// TestResolveAgentTier_FallsThroughToSecond pins the failover: a full first
// tier (slots consumed by dispatched rows) resolves to the second tier.
func TestResolveAgentTier_FallsThroughToSecond(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, rtB, _, tierB := tierTestSetup(t, 1)
	svc := newTierTestService(pool, qq)
	// Occupy the single slot with a running task on tier A's runtime.
	var blockerID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, agent.RuntimeID).Scan(&blockerID); err != nil {
		t.Fatalf("insert blocker: %v", err)
	}
	t.Cleanup(func() {
		_, _ = qq.CancelAgentTask(context.Background(), blockerID)
	})
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := util.UUIDToString(res.runtimeID); got != rtB {
		t.Errorf("runtime = %s, want tier-B runtime %s", got, rtB)
	}
	if got := util.UUIDToString(res.tierID); got != tierB {
		t.Errorf("tier = %s, want %s", got, tierB)
	}
}

// TestResolveAgentTier_BothFullStaysQueued pins the backpressure contract:
// when every selected tier is full the resolver returns errNoTierCapacity so
// the caller keeps the task queued instead of failing it.
func TestResolveAgentTier_BothFullStaysQueued(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, _, _, _ := tierTestSetup(t, 1)
	svc := newTierTestService(pool, qq)
	selections, err := qq.ListAgentTiers(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	for _, sel := range selections {
		if _, err := qq.UpdateModelTier(ctx, db.UpdateModelTierParams{
			ID: sel.ID, Name: sel.Name, Idx: sel.Idx, RuntimeID: sel.RuntimeID,
			Provider: sel.Provider, Model: sel.Model,
			ThinkingLevel: sel.ThinkingLevel, ServiceTier: sel.ServiceTier,
			Cost: sel.Cost, Quality: sel.Quality,
			ProviderSlots: 1, Enabled: sel.Enabled,
		}); err != nil {
			t.Fatalf("set slots: %v", err)
		}
		var blockerID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, sel.RuntimeID).Scan(&blockerID); err != nil {
			t.Fatalf("insert blocker: %v", err)
		}
		t.Cleanup(func() {
			_, _ = qq.CancelAgentTask(context.Background(), blockerID)
		})
	}
	if _, err := svc.resolveAgentTier(ctx, agent); err != errNoTierCapacity {
		t.Fatalf("resolve err = %v, want errNoTierCapacity", err)
	}
}

// TestApplyTierResolution_LegacyAgentUntouched pins the regression contract:
// use_tiers=false agents return their own runtime with empty stamps.
func TestApplyTierResolution_LegacyAgentUntouched(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "legacy tester", fmt.Sprintf("legacy-tester-%d@multica.test", time.Now().UnixNano()))
	fx.WorkspaceID = fx.Workspace(t, "legacy ws", fmt.Sprintf("legacy-ws-%d", time.Now().UnixNano()))
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	rt := fx.Runtime(t, "legacy rt")
	agentID := fx.Agent(t, "legacy agent", rt)
	qq := db.New(pool)
	agent, err := qq.GetAgent(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	svc := newTierTestService(pool, qq)
	runtimeID, model, thinking, service, tierID, parked, queuedOnly, rerr := svc.applyTierResolution(ctx, agent)
	if rerr != nil {
		t.Fatalf("apply: %v", rerr)
	}
	if queuedOnly {
		t.Fatalf("legacy agent must not be queuedOnly")
	}
	if got := util.UUIDToString(runtimeID); got != rt {
		t.Errorf("runtime = %s, want %s", got, rt)
	}
	if model.Valid || thinking.Valid || service.Valid || tierID.Valid || parked.Valid {
		t.Errorf("legacy stamps must be empty, got %+v %+v %+v %+v parked=%+v", model, thinking, service, tierID, parked)
	}
}

// TestIsRuntimeClaimable is DB-free: offline, stale, and missing heartbeats
// never qualify; a fresh online runtime does.
func TestIsRuntimeClaimable(t *testing.T) {
	fresh := time.Now()
	stale := fresh.Add(-time.Duration(RuntimeClaimFreshnessSeconds+60) * time.Second)
	mk := func(status string, lastSeen time.Time, lastSeenValid bool) db.AgentRuntime {
		return db.AgentRuntime{
			Status:     status,
			LastSeenAt: pgtype.Timestamptz{Time: lastSeen, Valid: lastSeenValid},
			UpdatedAt:  pgtype.Timestamptz{Time: fresh, Valid: true},
		}
	}
	cases := []struct {
		name string
		rt   db.AgentRuntime
		want bool
	}{
		{"online fresh", mk("online", fresh, true), true},
		{"offline fresh", mk("offline", fresh, true), false},
		{"online stale", mk("online", stale, true), false},
		{"online no heartbeat falls back to updated_at", mk("online", time.Time{}, false), true},
	}
	for _, tc := range cases {
		if got := isRuntimeClaimable(tc.rt); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// Zero updated_at with no heartbeat never qualifies.
	zero := mk("online", time.Time{}, false)
	zero.UpdatedAt = pgtype.Timestamptz{}
	if isRuntimeClaimable(zero) {
		t.Errorf("zero timestamps: got true want false")
	}
}

// TestTierSweep_BothFullThenSlotFrees is the M-351 acceptance flow end to
// end at the service layer: both tiers full -> enqueue yields a queued row
// with no runtime -> free a slot -> RestampRuntimelessTierTasks stamps the
// row on the freed tier, and the row is claim-shaped (runtime + tier set).
func TestTierSweep_BothFullThenSlotFrees(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, _, _, tierA := tierTestSetup(t, 1)
	svc := newTierTestService(pool, qq)

	// Occupy both tiers: one active row on each tier runtime.
	tierBRow, err := qq.GetAgentRuntime(ctx, agent.RuntimeID)
	_ = tierBRow
	_ = err
	selections, err := qq.ListAgentTiers(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	if len(selections) != 2 {
		t.Fatalf("want 2 tiers, got %d", len(selections))
	}
	// Shrink tier B to 1 slot as well (fixture default is 99).
	for _, sel := range selections {
		if _, err := qq.UpdateModelTier(ctx, db.UpdateModelTierParams{
			ID: sel.ID, Name: sel.Name, Idx: sel.Idx, RuntimeID: sel.RuntimeID,
			Provider: sel.Provider, Model: sel.Model,
			ThinkingLevel: sel.ThinkingLevel, ServiceTier: sel.ServiceTier,
			Cost: sel.Cost, Quality: sel.Quality,
			ProviderSlots: 1, Enabled: sel.Enabled,
		}); err != nil {
			t.Fatalf("set slots: %v", err)
		}
	}
	var blockers []pgtype.UUID
	for _, sel := range selections {
		var bid pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, sel.RuntimeID).Scan(&bid); err != nil {
			t.Fatalf("insert blocker: %v", err)
		}
		blockers = append(blockers, bid)
	}
	t.Cleanup(func() {
		for _, bid := range blockers {
			_, _ = qq.CancelAgentTask(context.Background(), bid)
		}
	})

	// Enqueue with both tiers full: the row stays queued with no runtime.
	fxIssueID := util.MustParseUUID(newTierTestIssue(t, pool, agent))
	issueRow, err := qq.GetIssue(ctx, fxIssueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	task, err := svc.enqueueIssueTask(ctx, issueRow, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived)
	if err != nil {
		t.Fatalf("enqueue with full tiers: %v", err)
	}
	if task.Status != "queued" {
		t.Fatalf("status = %s, want queued", task.Status)
	}
	if task.RuntimeID.Valid {
		t.Fatalf("runtime stamped despite full tiers: %s", util.UUIDToString(task.RuntimeID))
	}
	if task.TierID.Valid {
		t.Fatalf("tier stamped despite full tiers")
	}

	// Free tier A's slot, then run the sweep.
	if _, err := qq.CancelAgentTask(ctx, blockers[0]); err != nil {
		t.Fatalf("free slot: %v", err)
	}
	blockers = blockers[1:]
	stamped := svc.RestampRuntimelessTierTasks(ctx)
	if len(stamped) != 1 {
		t.Fatalf("sweep stamped %d rows, want 1", len(stamped))
	}
	got := stamped[0]
	if got.ID != task.ID {
		t.Fatalf("sweep stamped %s, want %s", util.UUIDToString(got.ID), util.UUIDToString(task.ID))
	}
	if util.UUIDToString(got.RuntimeID) != rtA {
		t.Errorf("stamped runtime = %s, want tier-A %s", util.UUIDToString(got.RuntimeID), rtA)
	}
	if util.UUIDToString(got.TierID) != tierA {
		t.Errorf("stamped tier = %s, want %s", util.UUIDToString(got.TierID), tierA)
	}
	if !got.ModelOverride.Valid || got.ModelOverride.String != "model-a" {
		t.Errorf("modelOverride = %+v, want model-a", got.ModelOverride)
	}
	// Still-full sweep is a no-op: re-block tier A, sweep, nothing stamped.
	var reblock pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, util.MustParseUUID(rtA)).Scan(&reblock); err != nil {
		t.Fatalf("re-block: %v", err)
	}
	t.Cleanup(func() {
		_, _ = qq.CancelAgentTask(context.Background(), reblock)
	})
	if again := svc.RestampRuntimelessTierTasks(ctx); len(again) != 0 {
		t.Fatalf("sweep with full tiers stamped %d rows, want 0", len(again))
	}
}

// newTierTestIssue inserts a minimal issue assigned to the agent for enqueue tests.
func newTierTestIssue(t *testing.T, pool *pgxpool.Pool, agent db.Agent) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, assignee_id, number, position) VALUES ($1, 'tier sweep issue', 'todo', 'none', 'member', $2, $3, (SELECT COALESCE(MAX(number),0)+1 FROM issue WHERE workspace_id = $1), 0) RETURNING id`,
		agent.WorkspaceID, agent.OwnerID, agent.ID).Scan(&id)
	if err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, util.MustParseUUID(id))
	})
	return id
}
