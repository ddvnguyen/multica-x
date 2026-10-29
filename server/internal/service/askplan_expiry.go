// Package service — M-131: ask/plan timeout expiry and escalation.
//
// The sweeper half of the timeout M-123 shipped as storage + surface. One
// parked ask/plan at a time, when its effective timeout (project override →
// workspace default → built-in, resolved by handler.EffectiveAskPlanTimeoutSeconds
// and passed in so the comment can name it) has passed:
//
//  1. one transaction — chat-session lock (the order CancelTaskWithResult's
//     system branch uses), task re-check, evidence-row lock, the plain system
//     cancel, and both settles;
//  2. post-commit — the same side effects as any cancel;
//  3. escalation — a durable system comment on the issue, then the project
//     lead triggered programmatically (mention task for an agent lead, an
//     action-required inbox row for a member lead).
//
// Two invariants the card demands and the gate test pins:
//
//   - First answer wins. The expiry gate locks the card/plan ROW itself, so a
//     concurrent AnswerQuestionCard / ReviewPlan either commits first (the
//     sweep sees final status and does nothing) or blocks on our lock and then
//     loses its own CAS with a 409. Never both.
//   - The run is never cancelled outright and never resumed with a notice. Only
//     the parked row — which already stopped running at park time — is
//     cancelled; no resume task is ever enqueued from this path.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/inbox"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// errParkedAskPlanNotExpirable aborts the expiry transaction as a no-op, never
// a failure: the row is gone, no longer parked as an ask/plan, or its evidence
// is no longer open — i.e. a human answered (first answer wins) or another
// path already cancelled it. Callers map it to (false, nil).
var errParkedAskPlanNotExpirable = errors.New("parked ask/plan is not expirable")

// ExpireParkedAskPlanTask reaps one parked ask/plan whose effective timeout has
// passed (M-131). timeoutSeconds/timeoutSource are the sweeper's resolution
// (handler.EffectiveAskPlanTimeoutSeconds) and are carried into the escalation
// comment so the record says which number governed.
//
// Returns (true, nil) when this call cancelled the park and escalated,
// (false, nil) when the row was not an expirable park (including "already
// answered"), and an error only for real failures — a skipped row never fails
// the sweep.
func (s *TaskService) ExpireParkedAskPlanTask(ctx context.Context, taskID pgtype.UUID, timeoutSeconds int, timeoutSource string) (bool, error) {
	var cancelled db.AgentTaskQueue
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// Lock order mirrors CancelTaskWithResult's non-user branch (task.go):
		// chat session first, then the task row. The evidence-row gate sits
		// between them, so this path adds no deadlock pair — AnswerQuestionCard
		// locks the card alone, ReviewPlan reaches the plan row without the
		// session lock.
		if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
			return err
		}
		task, err := qtx.GetAgentTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errParkedAskPlanNotExpirable
		}
		if err != nil {
			return fmt.Errorf("load parked task: %w", err)
		}
		if task.Status != "blocked" || !task.WaitReason.Valid || task.WaitReason.String != WaitReasonAskPlan {
			return errParkedAskPlanNotExpirable
		}

		// M-131 amendment 2 — the gate is the evidence ROW itself, locked by
		// the parked task's trigger_evidence_ref_id:
		//
		//   open card   → we hold its row lock; AnswerQuestionCard's UPDATE
		//                 waits, then its status='open' CAS matches no row and
		//                 the human gets ErrQuestionCardClosed (409);
		//   in_review plan → likewise against ReviewPlan's status/revision CAS.
		//
		// Anything else (answered, withdrawn, verdicted, unknown kind, missing
		// ref) is a no-op: first answer wins, and an unparked row we do not
		// recognize is never reaped.
		if !task.TriggerEvidenceKind.Valid || !task.TriggerEvidenceRefID.Valid {
			slog.Warn("ask/plan expiry skipped: parked task carries no evidence ref",
				"task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
			return errParkedAskPlanNotExpirable
		}
		switch task.TriggerEvidenceKind.String {
		case EvidenceQuestionAnswer:
			card, err := qtx.GetAgentQuestionCardForUpdate(ctx, task.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotExpirable
			}
			if err != nil {
				return fmt.Errorf("lock parked question card: %w", err)
			}
			if card.Status != "open" {
				slog.Info("ask/plan expiry skipped: question card no longer open",
					"task_id", util.UUIDToString(task.ID), "card_id", util.UUIDToString(card.ID), "status", card.Status)
				return errParkedAskPlanNotExpirable
			}
		case EvidencePlanVerdict:
			plan, err := qtx.GetAgentPlanForUpdate(ctx, task.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotExpirable
			}
			if err != nil {
				return fmt.Errorf("lock parked plan: %w", err)
			}
			if plan.Status != "in_review" {
				slog.Info("ask/plan expiry skipped: plan no longer in_review",
					"task_id", util.UUIDToString(task.ID), "plan_id", util.UUIDToString(plan.ID), "status", plan.Status)
				return errParkedAskPlanNotExpirable
			}
		default:
			slog.Warn("ask/plan expiry skipped: unknown evidence kind",
				"task_id", util.UUIDToString(task.ID), "kind", task.TriggerEvidenceKind.String)
			return errParkedAskPlanNotExpirable
		}

		// The plain system cancel, exactly as CancelTaskWithResult takes it:
		// CancelAgentTask accepts 'blocked' (queries/agent.sql) and stamps
		// cancelled_by_type='system'. No error message or failure reason — the
		// escalation comment is the record of why.
		cancelled, err = qtx.CancelAgentTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errParkedAskPlanNotExpirable
		}
		if err != nil {
			return fmt.Errorf("cancel parked task: %w", err)
		}
		// Same three settles as every cancel path, same transaction: terminal
		// state, then withdraw the open card / cancel the open plan (the gate
		// above already proved one of them is live), then the chat pointer.
		if err := SettleTerminalTaskState(ctx, qtx, cancelled); err != nil {
			return err
		}
		if err := SettleParkedAskPlanForTasks(ctx, qtx, []db.AgentTaskQueue{cancelled}); err != nil {
			return err
		}
		if !cancelled.ChatSessionID.Valid {
			return nil
		}
		return qtx.AdvanceCancelledChatSessionPointer(ctx, cancelled.ID)
	})
	if errors.Is(err, errParkedAskPlanNotExpirable) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("expire parked ask/plan: %w", err)
	}

	// Post-commit, mirroring CancelTaskWithResult: chat finalize, analytics
	// capture, agent reconcile, broadcast, finish notify. Dropping the
	// CancelledChatMessage result is the only difference — no HTTP caller to
	// hand it to.
	slog.Info("parked ask/plan expired",
		"task_id", util.UUIDToString(cancelled.ID),
		"issue_id", util.UUIDToString(cancelled.IssueID),
		"evidence_kind", cancelled.TriggerEvidenceKind.String,
		"timeout_seconds", timeoutSeconds,
		"timeout_source", timeoutSource,
	)
	s.captureTaskCancelled(ctx, cancelled)
	_ = s.finalizeCancelledChatMessage(ctx, cancelled, CancelTaskOptions{})
	s.ReconcileAgentStatus(ctx, cancelled.AgentID)
	s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, cancelled)
	s.NotifyTaskFinished(cancelled)

	s.escalateExpiredAskPlan(ctx, cancelled, timeoutSeconds, timeoutSource)
	return true, nil
}

// escalateExpiredAskPlan posts the durable system comment and triggers the
// project lead (M-131). Every trigger here is best-effort: the comment is the
// record, and a logged failure never un-cancels anything.
//
// The comment carries NO mention:// links on purpose — the comment listener
// refuses mention parsing for system authors (cmd/server/notification_listeners.go),
// and a transcluded mention in a platform signal is exactly what that guard
// exists to stop. The lead is triggered programmatically below instead.
func (s *TaskService) escalateExpiredAskPlan(ctx context.Context, task db.AgentTaskQueue, timeoutSeconds int, timeoutSource string) {
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("ask/plan timeout escalation: load issue failed",
			"task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", err)
		return
	}

	// The project lead: member or agent (project.lead_type CHECK). A project
	// without a lead, no project at all, or an unreadable row degrades to the
	// comment alone — the contract names the lead, so an invented recipient is
	// worse than none (verdict on open question 1).
	var (
		leadType   string
		leadID     pgtype.UUID
		leadName   string
		leadLoaded bool
	)
	if issue.ProjectID.Valid {
		project, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          issue.ProjectID,
			WorkspaceID: issue.WorkspaceID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			slog.Warn("ask/plan timeout escalation: project gone, comment only",
				"issue_id", util.UUIDToString(issue.ID))
		case err != nil:
			slog.Warn("ask/plan timeout escalation: load project failed, comment only",
				"issue_id", util.UUIDToString(issue.ID), "error", err)
		default:
			if project.LeadType.Valid && project.LeadID.Valid {
				leadType, leadID, leadLoaded = project.LeadType.String, project.LeadID, true
				leadName = s.askPlanLeadName(ctx, project.LeadType.String, project.LeadID)
			}
		}
	} else {
		slog.Warn("ask/plan timeout escalation: issue has no project, comment only",
			"issue_id", util.UUIDToString(issue.ID))
	}

	created, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		// author_type='system', author_id=zero UUID — the shape every
		// platform-authored comment uses (migration 107; runtime_unusable_notice).
		AuthorType: "system",
		AuthorID:   pgtype.UUID{Valid: true},
		Content:    renderAskPlanTimeoutComment(task, timeoutSeconds, timeoutSource, leadType, leadName, leadLoaded),
		Type:       "system",
		ParentID:   pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("ask/plan timeout escalation: create system comment failed",
			"task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(issue.ID), "error", err)
		return
	}
	comment := created.Comment()
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventCommentCreated,
			WorkspaceID: util.UUIDToString(issue.WorkspaceID),
			ActorType:   "system",
			Payload: map[string]any{
				"comment": map[string]any{
					"id":          util.UUIDToString(comment.ID),
					"issue_id":    util.UUIDToString(comment.IssueID),
					"author_type": comment.AuthorType,
					"author_id":   util.UUIDToString(comment.AuthorID),
					"content":     comment.Content,
					"type":        comment.Type,
					"revision":    comment.Revision,
				},
				"issue_title":    issue.Title,
				"issue_revision": created.IssueRevision,
			},
		})
	}

	if !leadLoaded {
		slog.Warn("ask/plan timeout escalation: no project lead set; comment posted without a trigger",
			"issue_id", util.UUIDToString(issue.ID))
		return
	}
	switch leadType {
	case "agent":
		// OriginDerived, not OriginNamed: nothing here is a person writing the
		// lead's name, so the Triage guard must still apply (open question 2 —
		// a Triage issue gets the comment and no task, guard not bypassed).
		// Refusal (Triage, archived, fail-closed attribution) is logged and
		// dropped; the comment stands.
		if _, err := s.EnqueueTaskForMention(ctx, issue, leadID, comment.ID, OriginDerived); err != nil {
			slog.Warn("ask/plan timeout escalation: lead agent task not enqueued",
				"issue_id", util.UUIDToString(issue.ID), "lead_agent_id", util.UUIDToString(leadID), "error", err)
		}
	case "member":
		details, err := json.Marshal(map[string]any{
			"task_id":         util.UUIDToString(task.ID),
			"timeout_seconds": timeoutSeconds,
			"source":          timeoutSource,
		})
		if err != nil {
			slog.Warn("ask/plan timeout escalation: inbox details encode failed", "error", err)
			return
		}
		item, inserted, err := inbox.CreateIfEligible(ctx, s.Queries, db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   issue.WorkspaceID,
			RecipientType: "member",
			RecipientID:   leadID,
			// Existing type on purpose: 'agent_question' is already the
			// action_required ask row every workspace member got at park time;
			// the lead's escalation reuses it instead of minting a catalog
			// entry, addressed this time.
			Type:      "agent_question",
			Severity:  "action_required",
			IssueID:   issue.ID,
			Title:     "Ask/plan timeout: " + issue.Title,
			ActorType: pgtype.Text{String: "system", Valid: true},
			Details:   details,
		})
		if err != nil {
			slog.Warn("ask/plan timeout escalation: lead inbox creation failed",
				"issue_id", util.UUIDToString(issue.ID), "lead_user_id", util.UUIDToString(leadID), "error", err)
			return
		}
		if inserted && s.Bus != nil {
			s.Bus.Publish(events.Event{
				Type:        protocol.EventInboxNew,
				WorkspaceID: util.UUIDToString(issue.WorkspaceID),
				ActorType:   "system",
				Payload: map[string]any{"item": map[string]any{
					"id":             util.UUIDToString(item.ID),
					"workspace_id":   util.UUIDToString(item.WorkspaceID),
					"recipient_type": item.RecipientType,
					"recipient_id":   util.UUIDToString(item.RecipientID),
					"type":           item.Type,
					"severity":       item.Severity,
					"issue_id":       util.UUIDToPtr(item.IssueID),
					"title":          item.Title,
					"body":           util.TextToPtr(item.Body),
					"read":           item.Read,
					"archived":       item.Archived,
					"created_at":     util.TimestampToString(item.CreatedAt),
					"actor_type":     util.TextToPtr(item.ActorType),
					"actor_id":       util.UUIDToPtr(item.ActorID),
					"details":        json.RawMessage(item.Details),
				}},
			})
		}
	default:
		slog.Warn("ask/plan timeout escalation: unknown project lead type; comment only",
			"issue_id", util.UUIDToString(issue.ID), "lead_type", leadType)
	}
}

// askPlanLeadName resolves the lead's display name for the escalation comment.
// Best-effort: an unresolvable name leaves it empty and the comment still says
// which lead type it escalated to.
func (s *TaskService) askPlanLeadName(ctx context.Context, leadType string, leadID pgtype.UUID) string {
	switch leadType {
	case "agent":
		if agent, err := s.Queries.GetAgent(ctx, leadID); err == nil {
			return agent.Name
		}
	case "member":
		if user, err := s.Queries.GetUser(ctx, leadID); err == nil {
			return user.Name
		}
	}
	return ""
}

// renderAskPlanTimeoutComment is the durable record: what expired, how long it
// waited, which setting governed it, what was done, and who it escalated to.
// Plain text only — no mention links (see escalateExpiredAskPlan).
func renderAskPlanTimeoutComment(task db.AgentTaskQueue, timeoutSeconds int, timeoutSource, leadType, leadName string, leadLoaded bool) string {
	var b strings.Builder
	isPlan := task.TriggerEvidenceKind.Valid && task.TriggerEvidenceKind.String == EvidencePlanVerdict
	if isPlan {
		fmt.Fprintf(&b, "**Ask/plan timeout:** the plan on this run sat in review for the whole effective timeout of %ds (%s) with no verdict. It has been cancelled and the parked task cancelled with it — no resume was started, first verdict wins.\n", timeoutSeconds, timeoutSource)
	} else {
		fmt.Fprintf(&b, "**Ask/plan timeout:** the question card on this run waited %ds without an answer — the full effective timeout (%s). The card has been withdrawn and the parked task cancelled with it; no resume was started, first answer wins.\n", timeoutSeconds, timeoutSource)
	}
	switch {
	case !leadLoaded:
		b.WriteString("No project lead is set on this project, so nothing was triggered beyond this note.")
	case leadType == "agent" && leadName != "":
		fmt.Fprintf(&b, "Escalated to the project lead: %s (agent), whose run is queued on this issue.", leadName)
	case leadType == "agent":
		b.WriteString("Escalated to the project lead (agent): a run is queued on this issue.")
	case leadName != "":
		fmt.Fprintf(&b, "Escalated to the project lead: %s.", leadName)
	default:
		b.WriteString("Escalated to the project lead (member): an action-required inbox item was delivered.")
	}
	return b.String()
}
