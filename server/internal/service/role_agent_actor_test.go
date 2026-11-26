package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// roleForPilotNoTiers is the production Role shape from M-595: subtype role, a
// role-keyed tier list, and use_tiers left false (nothing consults that field on
// the Role run path). The false value is what exposes the G3 handoff gate.
func roleForPilotNoTiers(t *testing.T, s *tier423Setup, name, roleKey string) db.Agent {
	t.Helper()
	id := util.MustParseUUID(s.fx.Agent(t, name, "", testutil.Cols{
		"subtype":   "role",
		"tier_role": roleKey,
		"use_tiers": false,
	}))
	role, err := s.qq.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("load role %s: %v", name, err)
	}
	return role
}

// M-597 G2. An agent actor (the Orchestrator) owns no member id: the handler
// threads memberActorUserID, which is deliberately invalid for actor_type=agent.
// The enqueue therefore authorized the Role with no requester at all and failed
// closed, so an agent-initiated assignment never produced a run even though the
// write boundary had authorised the same assignment against the human
// originator. The pick must be judged by that originator — and only by it: an
// issue with no resolvable human stays denied.
func TestRoleEnqueueForAgentActorUsesOriginator(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m597 g2 rt")
	tier := s.tier(t, "m597-g2-tier", 0, rt, []string{"engineer"}, false, i32(60))
	role := roleForPilotNoTiers(t, s, "m597 g2 role", "engineer")

	issued := util.MustParseUUID(s.fx.Issue(t, "m597 g2 issue", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   util.UUIDToString(role.ID),
	}))
	issue, err := s.qq.GetIssue(ctx, issued)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	// This is the exact call dispatchIssueRun makes for an agent actor.
	task, err := s.svc.EnqueueTaskForIssueWithHandoff(ctx, issue, "", pgtype.UUID{})
	if err != nil {
		t.Fatalf("enqueue a Role for an agent actor: %v", err)
	}
	if util.UUIDToString(task.TierID) != util.UUIDToString(tier.ID) {
		t.Fatalf("tier = %s, want %s", util.UUIDToString(task.TierID), util.UUIDToString(tier.ID))
	}

	// No human anywhere in the chain: owner_fallback attributes the run, but it
	// is a degraded audit value and must not become a Role authorization grant.
	// An agent-created issue with no origin task carries no human anywhere in its
	// provenance chain.
	author := s.fx.Agent(t, "m597 g2 author", "")
	orphan := util.MustParseUUID(s.fx.Issue(t, "m597 g2 orphan issue", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   util.UUIDToString(role.ID),
		"creator_type":  "agent",
		"creator_id":    author,
	}))
	orphanIssue, err := s.qq.GetIssue(ctx, orphan)
	if err != nil {
		t.Fatalf("load orphan issue: %v", err)
	}
	if _, err := s.svc.EnqueueTaskForIssueWithHandoff(ctx, orphanIssue, "", pgtype.UUID{}); err == nil {
		t.Fatal("an agent actor with no resolvable originator must not run a Role")
	}
}

// M-597 G2, the other half: WillEnqueueRun is what the write path and the
// preview report, so it must reach the same verdict the enqueue would. A Role
// assignee is only runnable for a human the Role's own access rules admit.
func TestIssueRunTriggerDeclinesRoleWithoutAuthorizedRequester(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m597 trigger rt")
	s.tier(t, "m597-trigger-tier", 0, rt, []string{"engineer"}, false, i32(60))
	role := roleForPilotNoTiers(t, s, "m597 trigger role", "engineer")

	outsider := s.fx.User(t, "m597 outsider", fmt.Sprintf("m597-outsider-%d@multica.test", time.Now().UnixNano()))
	s.fx.Member(t, s.wsID, outsider, "member")

	issueID := util.MustParseUUID(s.fx.Issue(t, "m597 trigger issue", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   util.UUIDToString(role.ID),
	}))
	issue, err := s.qq.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	svc := &IssueService{Queries: s.qq, TxStarter: s.pool, TaskService: s.svc}
	in := IssueTriggerInput{Issue: issue, IsCreate: true, AssigneeChanged: true}

	for _, tc := range []struct {
		name      string
		requester pgtype.UUID
		want      bool
		reason    dispatch.ReasonCode
	}{
		{name: "the Role owner's originator queues", requester: util.MustParseUUID(s.fx.UserID), want: true},
		{name: "no originator is declined", reason: dispatch.ReasonInvocationNotAllowed},
		{name: "a member without access is declined", requester: util.MustParseUUID(outsider), reason: dispatch.ReasonInvocationNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trigger, ok := svc.WillEnqueueRun(ctx, in, IssueTriggerProbe{RoleRequester: tc.requester})
			if ok != tc.want {
				t.Fatalf("will enqueue = %v, want %v (decline %q)", ok, tc.want, trigger.DeclineReason)
			}
			if tc.want {
				if util.UUIDToString(trigger.AgentID) != util.UUIDToString(role.ID) {
					t.Fatalf("trigger agent = %s, want the Role", util.UUIDToString(trigger.AgentID))
				}
				return
			}
			if trigger.DeclineReason != tc.reason {
				t.Fatalf("decline = %q, want %q", trigger.DeclineReason, tc.reason)
			}
		})
	}
}
