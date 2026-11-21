package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func seedTierMember(t *testing.T, q *db.Queries, tierID, runtimeID, provider, model string, pos int32, slots int32) db.ModelTierMember {
	t.Helper()
	m, err := q.CreateModelTierMember(context.Background(), db.CreateModelTierMemberParams{
		TierID:        util.MustParseUUID(tierID),
		Position:      pos,
		Provider:      provider,
		Model:         model,
		RuntimeID:     util.MustParseUUID(runtimeID),
		ProviderSlots: slots,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return m
}

func TestResolveM481_MemberOrder(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, rtB, tierA, _ := tierTestSetup(t, 99)
	// Replace legacy single-tier behavior with explicit members: member 0 on rtA, member 1 on rtB.
	_ = qq.DeleteModelTierMembersByTier(ctx, util.MustParseUUID(tierA))
	m1 := seedTierMember(t, qq, tierA, rtA, "freebuff", "model-a", 0, 99)
	_ = seedTierMember(t, qq, tierA, rtB, "opencode-go", "model-a-go", 1, 99)
	svc := newTierTestService(pool, qq)
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if util.UUIDToString(res.memberID) != util.UUIDToString(m1.ID) {
		t.Fatalf("member = %s, want %s", util.UUIDToString(res.memberID), util.UUIDToString(m1.ID))
	}
	if util.UUIDToString(res.runtimeID) != rtA {
		t.Fatalf("runtime = %s, want %s", util.UUIDToString(res.runtimeID), rtA)
	}
}

func TestResolveM481_MemberCooldownIsolation(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, rtB, tierA, _ := tierTestSetup(t, 99)
	_ = qq.DeleteModelTierMembersByTier(ctx, util.MustParseUUID(tierA))
	m1 := seedTierMember(t, qq, tierA, rtA, "freebuff", "model-a", 0, 99)
	m2 := seedTierMember(t, qq, tierA, rtB, "opencode-go", "model-a-go", 1, 99)
	if err := qq.SetModelTierMemberCooldown(ctx, db.SetModelTierMemberCooldownParams{
		ID:            m1.ID,
		CooldownUntil: pgtype.Timestamptz{Time: time.Now().Add(15 * time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("cooldown: %v", err)
	}
	svc := newTierTestService(pool, qq)
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if util.UUIDToString(res.memberID) != util.UUIDToString(m2.ID) {
		t.Fatalf("member = %s, want %s", util.UUIDToString(res.memberID), util.UUIDToString(m2.ID))
	}
	if util.UUIDToString(res.runtimeID) != rtB {
		t.Fatalf("runtime = %s, want %s", util.UUIDToString(res.runtimeID), rtB)
	}
}

func TestResolveM481_AllMembersDown(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	qq, agent, rtA, rtB, tierA, tierB := tierTestSetup(t, 1)
	_ = qq.DeleteModelTierMembersByTier(ctx, util.MustParseUUID(tierA))
	_ = qq.DeleteModelTierMembersByTier(ctx, util.MustParseUUID(tierB))
	seedTierMember(t, qq, tierA, rtA, "freebuff", "model-a", 0, 1)
	seedTierMember(t, qq, tierB, rtB, "opencode-go", "model-b", 0, 1)
	// Occupy both runtimes.
	for _, rt := range []string{rtA, rtB} {
		var blockerID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority) VALUES ($1, $2, 'running', 1) RETURNING id`, agent.ID, util.MustParseUUID(rt)).Scan(&blockerID); err != nil {
			t.Fatalf("insert blocker: %v", err)
		}
		t.Cleanup(func() { _, _ = qq.CancelAgentTask(context.Background(), blockerID) })
	}
	svc := newTierTestService(pool, qq)
	if _, err := svc.resolveAgentTier(ctx, agent); err != errNoTierCapacity {
		t.Fatalf("err = %v, want errNoTierCapacity", err)
	}
}

func TestResolveM481_Member429CoolsOnlyMember(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "m481", fmt.Sprintf("m481-%d@multica.test", time.Now().UnixNano()))
	fx.WorkspaceID = fx.Workspace(t, "m481 ws", fmt.Sprintf("m481-ws-%d", time.Now().UnixNano()))
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	rtA := fx.Runtime(t, "rtA")
	rtB := fx.Runtime(t, "rtB")
	qq := db.New(pool)
	wsUUID := util.MustParseUUID(fx.WorkspaceID)
	tier, err := qq.CreateModelTier(ctx, db.CreateModelTierParams{
		WorkspaceID: wsUUID, Name: "deepseek", Idx: 0, RuntimeID: util.MustParseUUID(rtA),
		Provider: "freebuff", Model: "ds", ProviderSlots: 99, Enabled: true,
		AutoPick:     pgtype.Bool{Bool: true, Valid: true},
		CtxMaxTokens: pgtype.Int4{Int32: 280000, Valid: true}, CompactAtPct: pgtype.Int4{Int32: 75, Valid: true},
	})
	if err != nil {
		t.Fatalf("create tier: %v", err)
	}
	_ = qq.DeleteModelTierMembersByTier(ctx, tier.ID)
	m1 := seedTierMember(t, qq, util.UUIDToString(tier.ID), rtA, "freebuff", "ds", 0, 99)
	m2 := seedTierMember(t, qq, util.UUIDToString(tier.ID), rtB, "opencode-go", "ds", 1, 99)
	agentID := fx.Agent(t, "ag", rtA)
	agentUUID := util.MustParseUUID(agentID)
	_, _ = qq.SetAgentTiers(ctx, agentUUID)
	_, _ = qq.AddAgentTier(ctx, db.AddAgentTierParams{AgentID: agentUUID, TierID: tier.ID})
	agent, _ := qq.UpdateAgent(ctx, db.UpdateAgentParams{ID: agentUUID, UseTiers: pgtype.Bool{Bool: true, Valid: true}})
	svc := newTierTestService(pool, qq)
	// Simulate a 429 cooling only member 1.
	if err := qq.SetModelTierMemberCooldown(ctx, db.SetModelTierMemberCooldownParams{
		ID: m1.ID, CooldownUntil: pgtype.Timestamptz{Time: time.Now().Add(DefaultTierCooldown), Valid: true},
	}); err != nil {
		t.Fatalf("cooldown: %v", err)
	}
	res, err := svc.resolveAgentTier(ctx, agent)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if util.UUIDToString(res.memberID) != util.UUIDToString(m2.ID) {
		t.Fatalf("expected member2, got %s", util.UUIDToString(res.memberID))
	}
	_ = taskfailure.ReasonAgentProviderCapacityOrRateLimit
}
