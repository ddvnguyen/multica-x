package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"log/slog"
)

// MaybeContinueUnfinishedStep is invoked after a clean task completion. If the
// issue is still at the step the run was handed, no wakeup is armed and no other
// task is pending, it queues a single continuation run (2-min delay). Otherwise
// it is a no-op. Failures are logged, never returned to the caller.
func (s *TaskService) MaybeContinueUnfinishedStep(ctx context.Context, task db.AgentTaskQueue) {
	if !task.WorkflowStep.Valid || task.WorkflowStep.String == "" {
		return
	}
	if task.TriggerCommentID.Valid {
		// comment/mention consult: must stay quiet.
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("continue-unfinished: load issue", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	if issue.Status != task.WorkflowStep.String {
		return
	}
	if !issue.AssigneeID.Valid || issue.AssigneeID != task.AgentID {
		return
	}
	switch issue.Status {
	case "done", "cancelled", "in_review", "wait_for_decision", "blocked":
		return
	}
	// no pending task on issue
	if pending, err := s.Queries.HasPendingTaskForIssue(ctx, issue.ID); err == nil && pending {
		return
	}
	// no pending wakeup
	if wks, err := s.Queries.ListWaitingWakeups(ctx, db.ListWaitingWakeupsParams{IssueID: issue.ID, AgentID: task.AgentID}); err == nil && len(wks) > 0 {
		return
	}

	// workflow switch (default on if missing)
	// best-effort: look up project workflow
	// (skipped if no project/workflow; defaults on)

	fireAt := pgtype.Timestamptz{Time: time.Now().Add(2 * time.Minute), Valid: true}
	handoff := "Your previous run ended without moving the issue or arming a wakeup. Continue the next unfinished part, then either move the issue with the step's command or arm a wakeup for what you wait on."
	cont, err := s.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, handoff, pgtype.UUID{}, task.ID, fireAt, OriginDerived)
	if err != nil {
		slog.Warn("continue-unfinished: enqueue failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	// stamp workflow step on continuation task
	if err := s.Queries.SetTaskWorkflowStep(ctx, db.SetTaskWorkflowStepParams{
		WorkflowStep: pgtype.Text{String: task.WorkflowStep.String, Valid: true},
		ID:           cont.ID,
	}); err != nil {
		slog.Warn("continue-unfinished: stamp workflow step", "error", err)
	}
	slog.Info("continue-unfinished: queued continuation", "task_id", util.UUIDToString(task.ID), "continuation", util.UUIDToString(cont.ID))
}
