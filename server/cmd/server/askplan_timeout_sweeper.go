package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// askPlanTimeoutSweepInterval is two ticks a minute: the tightest effective
	// timeout is the validator's 60s floor (interactionsMinTimeoutSeconds), so
	// a park escalates no later than ~90s after its deadline, and the default
	// 600s wait lands within half a minute of expiry. Ticks that overlap a
	// slow round are dropped by runPeriodicSweep.
	askPlanTimeoutSweepInterval = 30 * time.Second
	// askPlanTimeoutSweepBatch bounds one round. Live parks are rare — one per
	// parked run — and every candidate older than its timeout is reaped in the
	// same round, so 50 is generous headroom, not a drain rate.
	askPlanTimeoutSweepBatch = 50
)

// runAskPlanTimeoutSweeper is M-131: it reaps parked ask/plan tasks whose
// effective timeout has passed and escalates each to the project lead. Started
// beside the other background sweepers, bound to sweepCtx so shutdown cancels
// it like the rest.
func runAskPlanTimeoutSweeper(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) {
	runPeriodicSweep(ctx, askPlanTimeoutSweepInterval, func() {
		expired, err := sweepExpiredAskPlanParks(ctx, queries, taskSvc)
		if err != nil {
			slog.Warn("ask/plan timeout sweeper: round failed", "error", err)
			return
		}
		if expired > 0 {
			slog.Info("ask/plan timeout sweeper: escalated expired parks", "count", expired)
		}
	})
}

// sweepExpiredAskPlanParks is one round: candidates with live evidence, oldest
// park first, each judged against its own project's effective timeout —
// resolved by the same handler.EffectiveAskPlanTimeoutSeconds the settings
// surfaces answer, so what the panel shows is exactly what expires here.
// A row that fails is logged and left for the next tick; it never aborts the
// round.
func sweepExpiredAskPlanParks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) (int, error) {
	candidates, err := queries.ListParkedAskPlanSweepCandidates(ctx, askPlanTimeoutSweepBatch)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	expired := 0
	for _, c := range candidates {
		if !c.ParkedAt.Valid {
			// Unreachable: COALESCE over two NOT NULL stamps. Skip rather than
			// treat an unknown clock as expired — never reap what we cannot date.
			continue
		}
		seconds, source := handler.EffectiveAskPlanTimeoutSeconds(c.ProjectSettings, c.WorkspaceSettings)
		if now.Sub(c.ParkedAt.Time) < time.Duration(seconds)*time.Second {
			continue
		}
		done, err := taskSvc.ExpireParkedAskPlanTask(ctx, c.TaskID, seconds, source.String())
		if err != nil {
			slog.Warn("ask/plan timeout sweeper: expire failed",
				"task_id", util.UUIDToString(c.TaskID),
				"issue_id", util.UUIDToString(c.IssueID),
				"error", err)
			continue
		}
		if done {
			expired++
		}
	}
	return expired, nil
}
