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
	// 300s wait lands within half a minute of expiry. Ticks that overlap a
	// slow round are dropped by runPeriodicSweep.
	askPlanTimeoutSweepInterval = 30 * time.Second
	// askPlanTimeoutSweepBatch bounds one round. Live parks are rare — one per
	// parked run — and every candidate older than its timeout is reaped in the
	// same round, so 50 is generous headroom, not a drain rate.
	askPlanTimeoutSweepBatch = 50
)

// runAskPlanTimeoutSweeper is M-131/M-332: it escalates each parked ask/plan
// whose effective timeout has passed to the project lead, once, and then leaves
// the card open and the task parked until the Owner answers. Started beside the
// other background sweepers, bound to sweepCtx so shutdown cancels it like the
// rest.
func runAskPlanTimeoutSweeper(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) {
	runPeriodicSweep(ctx, askPlanTimeoutSweepInterval, func() {
		expired, err := sweepExpiredAskPlanParks(ctx, queries, taskSvc)
		if err != nil {
			slog.Warn("ask/plan timeout sweeper: round failed", "error", err)
			return
		}
		if expired > 0 {
			slog.Info("ask/plan timeout sweeper: escalated overdue parks", "count", expired)
		}
	})
}

// sweepExpiredAskPlanParks is one round: candidates with live, not-yet-escalated
// evidence, oldest park first, each judged against its own project's effective
// timeout — resolved by the same handler.EffectiveAskPlanTimeoutSeconds the
// settings surfaces answer, so what the panel shows is exactly what escalates
// here. A row that fails is logged and left for the next tick; it never aborts
// the round. Escalating does not remove the park from the workspace — it only
// marks the evidence row, so the next round sees it as already escalated and
// leaves it for the Owner.
func sweepExpiredAskPlanParks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) (int, error) {
	expired, _, err := sweepAskPlanParks(ctx, queries, taskSvc)
	return expired, err
}

// sweepAskPlanParks is one round with two independent branches per candidate:
// the M-362 reminder at ~50% of the effective timeout (fires once, never
// after resolution) and the M-131/M-332 escalation past the full timeout.
// The candidate set only holds live, not-yet-escalated evidence with the park
// clock in ParkedAt, so both branches judge against the same per-project
// effective timeout — resolved by the same
// handler.EffectiveAskPlanTimeoutSeconds the settings surfaces answer. A row
// that fails is logged and left for the next tick; it never aborts the
// round. Escalating only marks the evidence row, so the next round sees it as
// already escalated and leaves it for the Owner; reminding only stamps
// reminded_at, so a reminded row never re-reminds.
func sweepAskPlanParks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) (expired, reminded int, err error) {
	candidates, err := queries.ListParkedAskPlanSweepCandidates(ctx, askPlanTimeoutSweepBatch)
	if err != nil {
		return 0, 0, err
	}
	now := time.Now()
	for _, c := range candidates {
		if !c.ParkedAt.Valid {
			// Unreachable: COALESCE over two NOT NULL stamps. Skip rather than
			// treat an unknown clock as expired — never reap what we cannot date.
			continue
		}
		seconds, source := handler.EffectiveAskPlanTimeoutSeconds(c.ProjectSettings, c.WorkspaceSettings)
		elapsed := now.Sub(c.ParkedAt.Time)
		if elapsed >= time.Duration(seconds)*time.Second/2 && elapsed < time.Duration(seconds)*time.Second {
			done, rerr := taskSvc.RemindParkedAskPlanTask(ctx, c.TaskID, seconds)
			if rerr != nil {
				slog.Warn("ask/plan timeout sweeper: remind failed",
					"task_id", util.UUIDToString(c.TaskID),
					"issue_id", util.UUIDToString(c.IssueID),
					"error", rerr)
			} else if done {
				reminded++
			}
		}
		if elapsed < time.Duration(seconds)*time.Second {
			continue
		}
		done, err := taskSvc.EscalateParkedAskPlanTask(ctx, c.TaskID, seconds, source.String())
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
	return expired, reminded, nil
}
