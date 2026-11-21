package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// tierTestSetup builds a workspace with two online runtimes and two tiers
// (idx 0 on rtA with `slotsA` provider slots, idx 1 on rtB with 99 slots),
// plus a use_tiers agent selecting both. Returns the ids the resolver tests
// need. Claim-fence tests use the same shape through the handler package.
func tierTestSetup(t *testing.T, slotsA int32) (q *db.Queries, agent db.Agent, rtA, rtB, tierA, tierB string) {
	qq, agent, rtA, rtB, tierA, tierB, _ := tierTestSetupWithWorkspace(t, slotsA, "")
	return qq, agent, rtA, rtB, tierA, tierB
}

// tierTestSetupWithWorkspace is tierTestSetup that also reports the workspace
// id, so enqueue tests can seed their issue in the same workspace. Empty
// wantWs skips the check; otherwise the ids must match.
func tierTestSetupWithWorkspace(t *testing.T, slotsA int32, wantWs string) (q *db.Queries, agent db.Agent, rtA, rtB, tierA, tierB, wsID string) {
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
	if wantWs != "" && fx.WorkspaceID != wantWs {
		t.Fatalf("workspace = %s, want %s", fx.WorkspaceID, wantWs)
	}
	return qq, updated, rtA, rtB, tierA, tierB, fx.WorkspaceID
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

// TestTierCoolingDown is DB-free: a future deadline cools, a past one and a
// NULL one do not (the tier re-enters idx order by itself on expiry).
func TestTierCoolingDown(t *testing.T) {
	now := time.Now()
	if !tierCoolingDown(pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}, now) {
		t.Errorf("future deadline must cool")
	}
	if tierCoolingDown(pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}, now) {
		t.Errorf("expired deadline must not cool")
	}
	if tierCoolingDown(pgtype.Timestamptz{}, now) {
		t.Errorf("NULL deadline must not cool")
	}
}

// TestResolveAgentTier_SkipsCoolingTier pins the M-352 gate in the resolver:
// an idx-0 tier inside its cooldown is passed over for idx 1, and is picked
// again the moment its deadline expires — no manual clear needed.
func TestResolveAgentTier_SkipsCoolingTier(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, rtB, tierA, _ := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)

	if err := qq.SetModelTierCooldown(ctx, db.SetModelTierCooldownParams{
		ID:            util.MustParseUUID(tierA),
		CooldownUntil: pgtype.Timestamptz{Time: time.Now().Add(DefaultTierCooldown), Valid: true},
	}); err != nil {
		t.Fatalf("arm cooldown: %v", err)
	}
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve with cooling tier: %v", err)
	}
	if got := util.UUIDToString(res.runtimeID); got != rtB {
		t.Errorf("runtime = %s, want tier-B runtime %s (idx 0 is cooling)", got, rtB)
	}

	// Expire the deadline: the same tier leads again.
	if err := qq.SetModelTierCooldown(ctx, db.SetModelTierCooldownParams{
		ID:            util.MustParseUUID(tierA),
		CooldownUntil: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true},
	}); err != nil {
		t.Fatalf("expire cooldown: %v", err)
	}
	res, err = svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve after cooldown: %v", err)
	}
	if got := util.UUIDToString(res.runtimeID); got != rtA {
		t.Errorf("runtime after expiry = %s, want tier-A runtime %s", got, rtA)
	}
}

// TestFailTaskArmsTierCooldown pins the recording half of M-352: a tier-stamped
// task that fails with a capacity/rate-limit or quota reason arms that tier's
// cooldown for the default window, in the same transaction as the fail. A
// tier-less task and an unrelated reason arm nothing.
func TestFailTaskArmsTierCooldown(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, _, tierA, _ := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	tierUUID := util.MustParseUUID(tierA)

	cooldownOf := func() pgtype.Timestamptz {
		t.Helper()
		var until pgtype.Timestamptz
		if err := pool.QueryRow(ctx, `SELECT cooldown_until FROM model_tier WHERE id = $1`, tierUUID).Scan(&until); err != nil {
			t.Fatalf("read cooldown: %v", err)
		}
		return until
	}
	clearCooldown := func() {
		t.Helper()
		if err := qq.SetModelTierCooldown(ctx, db.SetModelTierCooldownParams{ID: tierUUID}); err != nil {
			t.Fatalf("clear cooldown: %v", err)
		}
	}
	// failTierTask inserts a running row carrying tierID (NULL when invalid)
	// and fails it with reason through the real FailTask path.
	failTierTask := func(reason string, tierID pgtype.UUID) {
		t.Helper()
		var taskID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue
				(agent_id, runtime_id, status, priority, tier_id, attempt, max_attempts, started_at)
			VALUES ($1, $2, 'running', 0, $3, 1, 1, now())
			RETURNING id`, agent.ID, agent.RuntimeID, tierID).Scan(&taskID); err != nil {
			t.Fatalf("insert tier task: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
		if _, err := svc.FailTask(ctx, taskID, "provider refused", "", "", "", reason, false, "", ""); err != nil {
			t.Fatalf("FailTask(%s): %v", reason, err)
		}
	}

	for _, reason := range []string{
		taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(),
		taskfailure.ReasonAgentProviderQuotaLimit.String(),
	} {
		clearCooldown()
		before := time.Now()
		failTierTask(reason, tierUUID)
		until := cooldownOf()
		if !until.Valid {
			t.Fatalf("%s: cooldown_until is NULL, want an armed deadline", reason)
		}
		wantMin, wantMax := before.Add(DefaultTierCooldown), time.Now().Add(DefaultTierCooldown)
		if until.Time.Before(wantMin) || until.Time.After(wantMax) {
			t.Errorf("%s: cooldown_until = %s, want ~%s (+%s)", reason, until.Time, wantMin, DefaultTierCooldown)
		}
	}

	// A non-provider failure says the work failed, not the provider: no cooldown.
	clearCooldown()
	failTierTask(taskfailure.ReasonAgentProviderAuthOrAccess.String(), tierUUID)
	if until := cooldownOf(); until.Valid {
		t.Errorf("auth failure armed a cooldown: %s", until.Time)
	}

	// A tier-less task (legacy agent) has no tier to cool.
	clearCooldown()
	failTierTask(taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(), pgtype.UUID{})
	if until := cooldownOf(); until.Valid {
		t.Errorf("tier-less failure armed a cooldown: %s", until.Time)
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
	res, parked, queuedOnly, rerr := svc.applyTierResolution(ctx, agent)
	if rerr != nil {
		t.Fatalf("apply: %v", rerr)
	}
	if queuedOnly {
		t.Fatalf("legacy agent must not be queuedOnly")
	}
	if got := util.UUIDToString(res.runtimeID); got != rt {
		t.Errorf("runtime = %s, want %s", got, rt)
	}
	if res.modelOverride.Valid || res.thinkingLevel.Valid || res.serviceTier.Valid || res.tierID.Valid || parked.Valid {
		t.Errorf("legacy stamps must be empty, got %+v %+v %+v %+v parked=%+v", res.modelOverride, res.thinkingLevel, res.serviceTier, res.tierID, parked)
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
	qq, agent, rtA, _, tierA, _ := tierTestSetup(t, 1)
	svc := newTierTestService(pool, qq)

	// Occupy both tiers: one active row on each tier runtime.
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

// TestResolveExplicitTier_PinsNamedTier pins the M-359 explicit pick: naming
// tier-b stamps tier-b even though tier-a (idx 0) has free capacity — no
// auto ranking applies.
func TestResolveExplicitTier_PinsNamedTier(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, rtB, _, tierB := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	wsID := agent.WorkspaceID
	res, err := svc.resolveExplicitTier(ctx, agent, wsID, "tier-b")
	if err != nil {
		t.Fatalf("resolve explicit: %v", err)
	}
	if got := util.UUIDToString(res.tierID); got != tierB {
		t.Errorf("tier = %s, want %s", got, tierB)
	}
	if got := util.UUIDToString(res.runtimeID); got != rtB {
		t.Errorf("runtime = %s, want tier-B runtime %s", got, rtB)
	}
	if !res.hasCapacity {
		t.Errorf("explicit resolve must report capacity")
	}
	_ = pool
}

// TestResolveExplicitTier_CaseInsensitive pins suffix normalization: the
// label suffix `TIER-B` resolves like `tier-b`.
func TestResolveExplicitTier_CaseInsensitive(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, _, _, tierB := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	res, err := svc.resolveExplicitTier(ctx, agent, agent.WorkspaceID, "TIER-B")
	if err != nil {
		t.Fatalf("resolve explicit uppercase: %v", err)
	}
	if got := util.UUIDToString(res.tierID); got != tierB {
		t.Errorf("tier = %s, want %s", got, tierB)
	}
	_ = pool
}

// TestResolveExplicitTier_UnknownDisabledUnselected pins the M-359 block:
// an unknown name, a disabled tier, and a tier not in the agent's selection
// all return errUnknownTier (tier_unavailable, no fallback, no queue).
func TestResolveExplicitTier_UnknownDisabledUnselected(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, _, _, _ := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	wsID := agent.WorkspaceID

	if _, err := svc.resolveExplicitTier(ctx, agent, wsID, "no-such-tier"); err != errUnknownTier {
		t.Errorf("unknown tier err = %v, want errUnknownTier", err)
	}

	selections, err := qq.ListAgentTiers(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	disabled := selections[0]
	if _, err := qq.UpdateModelTier(ctx, db.UpdateModelTierParams{
		ID: disabled.ID, Name: disabled.Name, Idx: disabled.Idx, RuntimeID: disabled.RuntimeID,
		Provider: disabled.Provider, Model: disabled.Model,
		ThinkingLevel: disabled.ThinkingLevel, ServiceTier: disabled.ServiceTier,
		Cost: disabled.Cost, Quality: disabled.Quality,
		ProviderSlots: disabled.ProviderSlots, Enabled: false,
	}); err != nil {
		t.Fatalf("disable tier: %v", err)
	}
	if _, err := svc.resolveExplicitTier(ctx, agent, wsID, disabled.Name); err != errUnknownTier {
		t.Errorf("disabled tier err = %v, want errUnknownTier", err)
	}

	// Tier outside the selection: create a third tier the agent never selected.
	rtExtra := selections[0].RuntimeID
	extra, err := qq.CreateModelTier(ctx, db.CreateModelTierParams{
		WorkspaceID: wsID, Name: "tier-extra", Idx: 2, RuntimeID: rtExtra,
		Provider: "codebuddy", Model: "model-x", ProviderSlots: 99, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create extra tier: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM model_tier WHERE id = $1`, extra.ID)
	})
	if _, err := svc.resolveExplicitTier(ctx, agent, wsID, "tier-extra"); err != errUnknownTier {
		t.Errorf("unselected tier err = %v, want errUnknownTier", err)
	}
}

// TestResolveExplicitTier_FullTierParks pins the M-359 no-fallback rule: a
// named tier with no free slot returns errNoExplicitTierCapacity (the caller
// parks the row on THAT tier — never the next one).
func TestResolveExplicitTier_FullTierParks(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, _, _, _ := tierTestSetup(t, 99)
	svc := newTierTestService(pool, qq)
	wsID := agent.WorkspaceID

	selections, err := qq.ListAgentTiers(ctx, agent.ID)
	if err != nil {
		t.Fatalf("list tiers: %v", err)
	}
	var tierA db.ModelTier
	for _, sel := range selections {
		if sel.Idx == 0 {
			tierA = sel
		}
	}
	if _, err := qq.UpdateModelTier(ctx, db.UpdateModelTierParams{
		ID: tierA.ID, Name: tierA.Name, Idx: tierA.Idx, RuntimeID: tierA.RuntimeID,
		Provider: tierA.Provider, Model: tierA.Model,
		ThinkingLevel: tierA.ThinkingLevel, ServiceTier: tierA.ServiceTier,
		Cost: tierA.Cost, Quality: tierA.Quality,
		ProviderSlots: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("set slots: %v", err)
	}
	var blockerID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, tierA.RuntimeID).Scan(&blockerID); err != nil {
		t.Fatalf("insert blocker: %v", err)
	}
	t.Cleanup(func() {
		_, _ = qq.CancelAgentTask(context.Background(), blockerID)
	})
	if _, err := svc.resolveExplicitTier(ctx, agent, wsID, tierA.Name); err != errNoExplicitTierCapacity {
		t.Errorf("full tier err = %v, want errNoExplicitTierCapacity", err)
	}
}

// TestEnqueueMentionWithTier_ExplicitStampsAndMarks pins the stamp: an
// explicit pick writes tier_id + tier_explicit on the queued row.
func TestEnqueueMentionWithTier_ExplicitStampsAndMarks(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, _, rtB, _, tierB, wsID := tierTestSetupWithWorkspace(t, 99, "")
	svc := newTierTestService(pool, qq)
	userFx := testutil.New(pool, "", "")
	userID := userFx.User(t, "explicit tier user", fmt.Sprintf("explicit-tier-%d@multica.test", time.Now().UnixNano()))
	fx := testutil.New(pool, wsID, userID)
	issueID := util.MustParseUUID(fx.Issue(t, "explicit tier stamp"))
	var issue db.Issue
	if err := pool.QueryRow(ctx, `SELECT id, workspace_id, assignee_id, assignee_type, priority FROM issue WHERE id = $1`, issueID).Scan(&issue.ID, &issue.WorkspaceID, &issue.AssigneeID, &issue.AssigneeType, &issue.Priority); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	tierUUID := util.MustParseUUID(tierB)
	task, err := svc.EnqueueTaskForMentionWithTier(ctx, issue, agent.ID, pgtype.UUID{}, OriginNamed, TierTriggerOverride{TierID: tierUUID})
	if err != nil {
		t.Fatalf("enqueue with tier: %v", err)
	}
	t.Cleanup(func() {
		_, _ = qq.CancelAgentTask(context.Background(), task.ID)
	})
	if got := util.UUIDToString(task.TierID); got != tierB {
		t.Errorf("task tier = %s, want %s", got, tierB)
	}
	if got := util.UUIDToString(task.RuntimeID); got != rtB {
		t.Errorf("task runtime = %s, want tier-B runtime %s", got, rtB)
	}
	if !task.TierExplicit {
		t.Errorf("task tier_explicit = false, want true")
	}
	if task.TierParked {
		t.Errorf("task tier_parked = true, want false (capacity free)")
	}
	_ = pool
}

func TestPresetFallbackPreservesRequestedTierAndRechecksOwnerOnRetry(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, rtB, tierA, tierB, wsID := tierTestSetupWithWorkspace(t, 1, "")
	svc := newTierTestService(pool, qq)
	tierAID := util.MustParseUUID(tierA)
	workspaceID := util.MustParseUUID(wsID)
	if _, err := pool.Exec(ctx, `UPDATE model_tier SET manual_only = TRUE WHERE id = $1`, tierAID); err != nil {
		t.Fatalf("mark preset tier manual-only: %v", err)
	}
	var presetID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent_preset (workspace_id, name, agent_id, tier_id) VALUES ($1, 'owner preset', $2, $3) RETURNING id`, workspaceID, agent.ID, tierAID).Scan(&presetID); err != nil {
		t.Fatalf("create preset: %v", err)
	}
	issueID := util.MustParseUUID(newTierTestIssue(t, pool, agent))
	issue, err := qq.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	blockers := make([]pgtype.UUID, 0, 100)
	for i := 0; i < 100; i++ {
		runtimeID := util.MustParseUUID(rtB)
		if i == 0 {
			runtimeID = util.MustParseUUID(rtA)
		}
		var blockerID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, runtimeID).Scan(&blockerID); err != nil {
			t.Fatalf("insert capacity blocker %d: %v", i, err)
		}
		blockers = append(blockers, blockerID)
	}
	var task db.AgentTaskQueue
	t.Cleanup(func() {
		for _, id := range blockers {
			_, _ = qq.CancelAgentTask(context.Background(), id)
		}
		_, _ = qq.CancelAgentTask(context.Background(), task.ID)
	})

	override := TierTriggerOverride{TierID: tierAID, PresetID: presetID, PresetAgentID: agent.ID, Requester: agent.OwnerID}
	task, err = svc.enqueueIssueTaskWithOverride(ctx, issue, pgtype.UUID{}, false, "", agent.OwnerID, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived, override)
	if err != nil {
		t.Fatalf("enqueue parked preset: %v", err)
	}
	if task.RuntimeID.Valid || !task.TierParked {
		t.Fatalf("task runtime=%v parked=%v, want runtime-less parked task", task.RuntimeID, task.TierParked)
	}
	var metadata map[string]any
	if err := json.Unmarshal(task.Context, &metadata); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	if metadata["requested_preset_id"] != util.UUIDToString(presetID) || metadata["requested_tier_id"] != tierA {
		t.Fatalf("requested metadata = %#v, want preset %s and tier %s", metadata, util.UUIDToString(presetID), tierA)
	}
	if metadata["preset_fallback_reason"] != "tier_no_capacity" {
		t.Fatalf("fallback reason = %#v, want tier_no_capacity", metadata["preset_fallback_reason"])
	}
	if _, err := svc.enqueueIssueTaskWithOverride(ctx, issue, pgtype.UUID{}, false, "", agent.OwnerID, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived, override); !errors.Is(err, ErrDuplicatePendingTask) {
		t.Fatalf("duplicate preset enqueue error = %v, want ErrDuplicatePendingTask", err)
	}

	if _, err := qq.CancelAgentTask(ctx, blockers[1]); err != nil {
		t.Fatalf("free fallback tier capacity: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE member SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, workspaceID, agent.OwnerID); err != nil {
		t.Fatalf("revoke owner role: %v", err)
	}
	if stamped := svc.RestampRuntimelessTierTasks(ctx); len(stamped) != 0 {
		t.Fatalf("restamp bypassed lost owner authorization: got %d tasks", len(stamped))
	}
	if _, err := pool.Exec(ctx, `UPDATE member SET role = 'owner' WHERE workspace_id = $1 AND user_id = $2`, workspaceID, agent.OwnerID); err != nil {
		t.Fatalf("restore owner role: %v", err)
	}
	type claimResult struct {
		task db.AgentTaskQueue
		err  error
	}
	start := make(chan struct{})
	stampResult := make(chan []db.AgentTaskQueue, 1)
	claim := make(chan claimResult, 1)
	go func() {
		<-start
		stampResult <- svc.RestampRuntimelessTierTasks(ctx)
	}()
	go func() {
		<-start
		claimed, err := qq.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID: agent.ID, RuntimeID: util.MustParseUUID(rtB), PrepareLeaseSecs: 60, RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
		claim <- claimResult{task: claimed, err: err}
	}()
	close(start)
	stamped := <-stampResult
	claimOutcome := <-claim
	if len(stamped) != 1 || stamped[0].ID != task.ID {
		t.Fatalf("restamp = %#v, want queued preset task", stamped)
	}
	if claimOutcome.err != nil && !errors.Is(claimOutcome.err, pgx.ErrNoRows) {
		t.Fatalf("concurrent claim error = %v", claimOutcome.err)
	}
	var actualTier, actualRuntime pgtype.UUID
	var status string
	var parked bool
	var contextJSON []byte
	if err := pool.QueryRow(ctx, `SELECT tier_id, runtime_id, status, tier_parked, context FROM agent_task_queue WHERE id = $1`, task.ID).Scan(&actualTier, &actualRuntime, &status, &parked, &contextJSON); err != nil {
		t.Fatalf("read task after restamp/claim race: %v", err)
	}
	if util.UUIDToString(actualTier) != tierB || util.UUIDToString(actualRuntime) != rtB || parked {
		t.Fatalf("actual fallback tier/runtime/parked = %s/%s/%v, want %s/%s/false", util.UUIDToString(actualTier), util.UUIDToString(actualRuntime), parked, tierB, rtB)
	}
	if status != "queued" && status != "dispatched" {
		t.Fatalf("status after restamp/claim race = %s, want queued or dispatched", status)
	}
	if claimOutcome.err == nil && (claimOutcome.task.ID != task.ID || claimOutcome.task.Status != "dispatched") {
		t.Fatalf("claimed task = %s/%s, want %s/dispatched", util.UUIDToString(claimOutcome.task.ID), claimOutcome.task.Status, util.UUIDToString(task.ID))
	}
	if !bytes.Contains(contextJSON, []byte(`"preset_fallback_reason":"tier_no_capacity"`)) {
		t.Fatalf("restamped context lost fallback reason: %s", contextJSON)
	}
}
