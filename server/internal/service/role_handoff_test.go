package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// M-597 G3. PrepareIssueHandoff accepts a runtime-less flag-on Role (its own
// check carries the exemption), but EnqueuePreparedIssueHandoffInTx re-validated
// `!RuntimeID.Valid && !UseTiers` without it, so a wait_for_decision answer that
// handed work back to a production-shaped Role failed with "handoff agent
// changed during preparation" after the tier had already been resolved.
func TestPreparedHandoffAcceptsRuntimeLessRole(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m597 g3 rt")
	tier := s.tier(t, "m597-g3-tier", 0, rt, []string{"engineer"}, false, i32(60))
	role := roleForPilotNoTiers(t, s, "m597 g3 role", "engineer")
	issueID := util.MustParseUUID(s.fx.Issue(t, "m597 g3 issue", testutil.Cols{}))
	issue, err := s.qq.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	actor := util.MustParseUUID(s.fx.UserID)

	prepared, err := s.svc.PrepareIssueHandoff(ctx, issue, role.ID, actor)
	if err != nil {
		t.Fatalf("prepare handoff for a runtime-less Role: %v", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	task, err := s.svc.EnqueuePreparedIssueHandoffInTx(ctx, s.qq.WithTx(tx), issue, pgtype.UUID{}, prepared)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("enqueue prepared handoff for a runtime-less Role: %v", err)
	}
	if util.UUIDToString(task.TierID) != util.UUIDToString(tier.ID) {
		t.Errorf("handoff task tier = %s, want the Role's tier %s",
			util.UUIDToString(task.TierID), util.UUIDToString(tier.ID))
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

// A flag-off Role keeps the pre-M-510 behaviour end to end: the transaction
// re-checks the flag, so a handoff prepared while it was on is still refused if
// it goes off in between.
func TestPreparedHandoffFlagOffRoleStillRefused(t *testing.T) {
	s := tier423Fixture(t)
	s.svc.FeatureFlags = roleFlagService(true, s.wsID)
	ctx := context.Background()
	rt := s.runtime(t, "m597 g3 off rt")
	s.tier(t, "m597-g3-off-tier", 0, rt, []string{"engineer"}, false, i32(60))
	role := roleForPilotNoTiers(t, s, "m597 g3 off role", "engineer")
	issueID := util.MustParseUUID(s.fx.Issue(t, "m597 g3 off issue", testutil.Cols{}))
	issue, err := s.qq.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	actor := util.MustParseUUID(s.fx.UserID)
	prepared, err := s.svc.PrepareIssueHandoff(ctx, issue, role.ID, actor)
	if err != nil {
		t.Fatalf("prepare handoff: %v", err)
	}

	s.svc.FeatureFlags = roleFlagService(false, s.wsID)
	if _, err := s.svc.PrepareIssueHandoff(ctx, issue, role.ID, actor); err == nil {
		t.Fatal("a flag-off Role must still be refused at preparation")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := s.svc.EnqueuePreparedIssueHandoffInTx(ctx, s.qq.WithTx(tx), issue, pgtype.UUID{}, prepared); err == nil {
		t.Fatal("a flag-off Role must not pass the handoff transaction gate")
	}
}
