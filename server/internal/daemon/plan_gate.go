package daemon

// The plan gate's caller (docs/plannotator-gate.md).
//
// A plan-gate task parks because the agent submitted a plan and needs a
// verdict. The server records the park and tells the daemon; the daemon is the
// only side that can put a reviewer in front of the plan, because the reviewer
// is a person at *this* machine. So the gate runs here, off the task's critical
// path: the park report has already been sent, the task is already blocked, and
// a reviewer who walks away costs a goroutine, not a claim.
//
// Only the verdict crosses back. The plan and its verdict live in Multica's
// tables; Plannotator renders the plan and then stops existing.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/plannotator"
)

// startPlanGate launches the gate for a task that just parked on a plan.
//
// It returns immediately: reportInterruptedTask is on the run path, and
// waiting for a person to finish reading a plan there would hold the reporting
// goroutine for as long as the review takes.
//
// The gate runs under the daemon's lifecycle context rather than
// context.Background(). The review process is a child of this daemon, and a
// daemon that exits while a review is open (shutdown, restart, self-update)
// must take it with it: an orphaned `plannotator` would keep serving a
// browser tab for a run that no longer exists, and its temp dir — which holds
// the plan text — would survive because the deferred cleanup never runs. A
// killed gate is not a verdict: the plan stays parked and the panel can still
// decide it.
func (d *Daemon) startPlanGate(taskID string, logger *slog.Logger) {
	if !d.cfg.PlanGateEnabled || strings.TrimSpace(taskID) == "" {
		return
	}
	go d.runPlanGate(d.daemonLifecycleCtx(), taskID, logger)
}

// runPlanGate reads the parked plan, runs one gate, and posts the verdict.
//
// Every failure before the verdict is logged and dropped. A gate that cannot
// run leaves the task exactly as parked — which is the same state the panel
// shows, and still approveable there — so a broken gate never blocks a run.
func (d *Daemon) runPlanGate(ctx context.Context, taskID string, logger *slog.Logger) {
	log := logger.With("task_id", taskID, "step", "plan_gate")

	plan, err := d.client.GetParkedPlan(ctx, taskID)
	if err != nil {
		log.Warn("plan gate: could not read the parked plan", "error", err)
		return
	}
	if !plan.Ready() {
		return
	}

	dir, err := os.MkdirTemp("", "multica-plan-gate-*")
	if err != nil {
		log.Warn("plan gate: no temp dir for the plan", "error", err)
		return
	}
	defer os.RemoveAll(dir)

	planFile := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(planFile, []byte(plan.Markdown), 0o600); err != nil {
		log.Warn("plan gate: could not write the plan", "error", err)
		return
	}
	resultFile := filepath.Join(dir, "verdict.json")

	verdict, err := plannotator.Run(ctx, plannotator.Config{Binary: d.cfg.PlanGateBinary}, plannotator.Request{
		Mode:       plannotator.ModePlan,
		Target:     planFile,
		ResultFile: resultFile,
	})
	if verdict != nil && verdict.SessionURL != "" {
		// The gate's own stderr is forwarded, but a structured line is what
		// survives a log shipper: it names the review a reader has to look at
		// when the plan is still parked and nobody remembers where it opened.
		log.Info("plan gate: review session", "url", verdict.SessionURL, "plan_id", plan.PlanID)
	}
	if err != nil {
		if ctx.Err() != nil {
			// The daemon is going away and took the review with it. That is the
			// documented outcome, not a failure: the plan stays parked.
			log.Info("plan gate: cancelled before a verdict", "plan_id", plan.PlanID)
			return
		}
		// "The gate broke" is not a reviewer's answer: the task stays parked.
		log.Warn("plan gate: no verdict", "error", err, "plan_id", plan.PlanID, "exit_code", exitCodeOf(verdict))
		return
	}

	submission := ParkedPlanVerdict{PlanID: plan.PlanID, BaseRevision: plan.BaseRevision()}
	switch verdict.Decision {
	case plannotator.DecisionApproved:
		submission.Verdict = "approve"
	case plannotator.DecisionAnnotated:
		submission.Verdict = "changes_requested"
		submission.Feedback = verdict.Feedback
	default:
		// Dismissed: the reviewer closed the review without deciding. Leaving
		// the plan parked is deliberate — a dismissal is not a denial, and
		// treating it as one would discard work the agent did by accident.
		log.Info("plan gate: reviewer closed the review without a verdict", "plan_id", plan.PlanID)
		return
	}

	resumeTask, err := d.client.SubmitParkedPlanVerdict(ctx, taskID, submission)
	if err != nil {
		if code := planGateConflictCode(err); code != "" {
			// A conflict means something else decided first — except for
			// run_already_queued, where the verdict WAS recorded and a run was
			// already queued to carry it. Logging the two the same way would
			// tell a reader the reviewer's approval was lost when it landed.
			if code == "run_already_queued" {
				log.Info("plan gate: verdict recorded; a run was already queued", "code", code, "plan_id", plan.PlanID)
				return
			}
			log.Info("plan gate: another decision landed first; leaving it to the winner", "code", code, "plan_id", plan.PlanID)
			return
		}
		log.Warn("plan gate: could not post the verdict", "error", err, "plan_id", plan.PlanID)
		return
	}
	log.Info("plan gate: verdict recorded", "verdict", submission.Verdict, "plan_id", plan.PlanID, "resume_task", resumeTask)
}

// exitCodeOf reads a verdict's exit status without requiring a non-nil verdict.
func exitCodeOf(v *plannotator.Verdict) int {
	if v == nil {
		return -1
	}
	return v.ExitCode
}
