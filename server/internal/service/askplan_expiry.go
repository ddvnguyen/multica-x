// Package service — M-332: the ask/plan escalation ladder.
//
// One parked ask/plan at a time, when its effective timeout (project override →
// workspace default → built-in, resolved by handler.EffectiveAskPlanTimeoutSeconds
// and passed in so the comment can name it) has passed:
//
//  1. one transaction — task re-check, the evidence row locked, and a
//     once-only CAS that stamps escalated_at. Nothing else is written: the
//     card stays open, the plan stays in review, the parked task stays
//     'blocked'. The run already stopped at park time and is not restarted
//     here;
//  2. post-commit — a durable system comment that states the ladder, then the
//     project lead triggered programmatically (mention task for an agent lead,
//     an action-required inbox row for a member lead). The lead may answer the
//     card on the Owner's behalf only when the approved plan or the Owner's
//     recorded decisions cover it, or it is reversible; no answer at all is
//     also a valid response.
//  3. the Owner's answer, whenever it comes, is still first-answer-wins and
//     resumes the original parked run.
//
// Two invariants the card demands and the tests pin:
//
//   - First answer wins. AnswerQuestionCard's CAS is on status='open' and this
//     path never writes the status, so a human answer before, during or after
//     the escalation records exactly one answer.
//   - Escalation happens once. The CAS on `escalated_at IS NULL` means a second
//     sweep round (or a racing one) sees no row and does nothing, so the lead is
//     triggered and the comment posted exactly once per card/plan.
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

// errParkedAskPlanNotEscalatable aborts the escalation transaction as a no-op,
// never a failure: the row is gone, no longer parked as an ask/plan, its
// evidence is no longer open, or it was already escalated — i.e. a human or the
// lead answered (first answer wins), another path cancelled it, or a previous
// round already triggered the lead. Callers map it to (false, nil).
var errParkedAskPlanNotEscalatable = errors.New("parked ask/plan is not escalatable")

// EscalateParkedAskPlanTask escalates one parked ask/plan whose effective
// timeout has passed (M-332). timeoutSeconds/timeoutSource are the sweeper's
// resolution (handler.EffectiveAskPlanTimeoutSeconds) and are carried into the
// escalation comment so the record says which number governed.
//
// Returns (true, nil) when this call escalated, (false, nil) when the row was
// not an escalatable park (including "already answered" and "already
// escalated"), and an error only for real failures — a skipped row never fails
// the sweep.
func (s *TaskService) EscalateParkedAskPlanTask(ctx context.Context, taskID pgtype.UUID, timeoutSeconds int, timeoutSource string) (bool, error) {
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		loaded, err := qtx.GetAgentTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errParkedAskPlanNotEscalatable
		}
		if err != nil {
			return fmt.Errorf("load parked task: %w", err)
		}
		if loaded.Status != "blocked" || !loaded.WaitReason.Valid || loaded.WaitReason.String != WaitReasonAskPlan {
			return errParkedAskPlanNotEscalatable
		}

		// The gate is the evidence ROW itself. Locking it keeps the
		// escalation's stamp and a concurrent AnswerQuestionCard /
		// ReviewPlan from interleaving: whichever commits first wins, and
		// the other's status CAS matches no row. Anything else (answered,
		// withdrawn, verdicted, unknown kind, missing ref) is a no-op — an
		// unparked row we do not recognize is never escalated.
		if !loaded.TriggerEvidenceKind.Valid || !loaded.TriggerEvidenceRefID.Valid {
			slog.Warn("ask/plan escalation skipped: parked task carries no evidence ref",
				"task_id", util.UUIDToString(loaded.ID), "issue_id", util.UUIDToString(loaded.IssueID))
			return errParkedAskPlanNotEscalatable
		}
		switch loaded.TriggerEvidenceKind.String {
		case EvidenceQuestionAnswer:
			card, err := qtx.GetAgentQuestionCardForUpdate(ctx, loaded.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotEscalatable
			}
			if err != nil {
				return fmt.Errorf("lock parked question card: %w", err)
			}
			if card.Status != "open" {
				slog.Info("ask/plan escalation skipped: question card no longer open",
					"task_id", util.UUIDToString(loaded.ID), "card_id", util.UUIDToString(card.ID), "status", card.Status)
				return errParkedAskPlanNotEscalatable
			}
			if card.EscalatedAt.Valid {
				return errParkedAskPlanNotEscalatable
			}
			if _, err := qtx.EscalateAgentQuestionCard(ctx, card.ID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return errParkedAskPlanNotEscalatable
				}
				return fmt.Errorf("escalate question card: %w", err)
			}
		case EvidencePlanVerdict:
			plan, err := qtx.GetAgentPlanForUpdate(ctx, loaded.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotEscalatable
			}
			if err != nil {
				return fmt.Errorf("lock parked plan: %w", err)
			}
			if plan.Status != "in_review" {
				slog.Info("ask/plan escalation skipped: plan no longer in_review",
					"task_id", util.UUIDToString(loaded.ID), "plan_id", util.UUIDToString(plan.ID), "status", plan.Status)
				return errParkedAskPlanNotEscalatable
			}
			if plan.EscalatedAt.Valid {
				return errParkedAskPlanNotEscalatable
			}
			if _, err := qtx.EscalateAgentPlan(ctx, plan.ID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return errParkedAskPlanNotEscalatable
				}
				return fmt.Errorf("escalate plan: %w", err)
			}
		default:
			slog.Warn("ask/plan escalation skipped: unknown evidence kind",
				"task_id", util.UUIDToString(loaded.ID), "kind", loaded.TriggerEvidenceKind.String)
			return errParkedAskPlanNotEscalatable
		}

		task = loaded
		return nil
	})
	if errors.Is(err, errParkedAskPlanNotEscalatable) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("escalate parked ask/plan: %w", err)
	}

	slog.Info("parked ask/plan escalated",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"evidence_kind", task.TriggerEvidenceKind.String,
		"timeout_seconds", timeoutSeconds,
		"timeout_source", timeoutSource,
	)
	s.escalateAskPlanToLead(ctx, task, timeoutSeconds, timeoutSource)
	return true, nil
}

// errParkedAskPlanNotRemindable aborts the reminder transaction as a no-op,
// never a failure: the row is gone, no longer parked as an ask/plan, its
// evidence is no longer open, or it was already reminded. Callers map it to
// (false, nil).
var errParkedAskPlanNotRemindable = errors.New("parked ask/plan is not remindable")

// RemindParkedAskPlanTask fires the M-362 mid-timeout reminder for one parked
// ask/plan: one transaction — task re-check, the evidence row locked, and a
// once-only CAS that stamps reminded_at — then post-commit one inbox bump per
// workspace member reusing the park's type and evidence details, with
// details stamped `reminder: true`. The card stays open, the plan stays in
// review, the parked task stays 'blocked'. M-131 is untouched: escalation
// keeps its own stamp and path.
//
// Returns (true, nil) when this call reminded, (false, nil) when the row was
// not a remindable park (including "already answered" and "already
// reminded"), and an error only for real failures.
func (s *TaskService) RemindParkedAskPlanTask(ctx context.Context, taskID pgtype.UUID, timeoutSeconds int) (bool, error) {
	var task db.AgentTaskQueue
	var isPlan bool
	var cardID, planID string
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		loaded, err := qtx.GetAgentTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errParkedAskPlanNotRemindable
		}
		if err != nil {
			return fmt.Errorf("load parked task: %w", err)
		}
		if loaded.Status != "blocked" || !loaded.WaitReason.Valid || loaded.WaitReason.String != WaitReasonAskPlan {
			return errParkedAskPlanNotRemindable
		}
		if !loaded.TriggerEvidenceKind.Valid || !loaded.TriggerEvidenceRefID.Valid {
			slog.Warn("ask/plan reminder skipped: parked task carries no evidence ref",
				"task_id", util.UUIDToString(loaded.ID), "issue_id", util.UUIDToString(loaded.IssueID))
			return errParkedAskPlanNotRemindable
		}
		switch loaded.TriggerEvidenceKind.String {
		case EvidenceQuestionAnswer:
			card, err := qtx.GetAgentQuestionCardForUpdate(ctx, loaded.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotRemindable
			}
			if err != nil {
				return fmt.Errorf("lock parked question card: %w", err)
			}
			if card.Status != "open" {
				return errParkedAskPlanNotRemindable
			}
			if card.RemindedAt.Valid {
				return errParkedAskPlanNotRemindable
			}
			if _, err := qtx.RemindAgentQuestionCard(ctx, card.ID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return errParkedAskPlanNotRemindable
				}
				return fmt.Errorf("remind question card: %w", err)
			}
			cardID = util.UUIDToString(card.ID)
		case EvidencePlanVerdict:
			plan, err := qtx.GetAgentPlanForUpdate(ctx, loaded.TriggerEvidenceRefID)
			if errors.Is(err, pgx.ErrNoRows) {
				return errParkedAskPlanNotRemindable
			}
			if err != nil {
				return fmt.Errorf("lock parked plan: %w", err)
			}
			if plan.Status != "in_review" {
				return errParkedAskPlanNotRemindable
			}
			if plan.RemindedAt.Valid {
				return errParkedAskPlanNotRemindable
			}
			if _, err := qtx.RemindAgentPlan(ctx, plan.ID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return errParkedAskPlanNotRemindable
				}
				return fmt.Errorf("remind plan: %w", err)
			}
			isPlan = true
			planID = util.UUIDToString(plan.ID)
		default:
			slog.Warn("ask/plan reminder skipped: unknown evidence kind",
				"task_id", util.UUIDToString(loaded.ID), "kind", loaded.TriggerEvidenceKind.String)
			return errParkedAskPlanNotRemindable
		}
		task = loaded
		return nil
	})
	if errors.Is(err, errParkedAskPlanNotRemindable) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remind parked ask/plan: %w", err)
	}
	s.remindAskPlanMembers(ctx, task, isPlan, cardID, planID, timeoutSeconds)
	return true, nil
}

// remindAskPlanMembers delivers the reminder bump post-commit: one inbox row
// per workspace member, reusing the park's type and evidence details with
// `reminder: true` stamped in, so the row renders like the original ask but
// never collides with its evidence identity. Best-effort like every inbox
// housekeeping path: a failure logs and the stamp stands (the Owner can still
// answer from the original row).
func (s *TaskService) remindAskPlanMembers(ctx context.Context, task db.AgentTaskQueue, isPlan bool, cardID, planID string, timeoutSeconds int) {
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("ask/plan reminder: load issue failed",
			"task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", err)
		return
	}
	members, err := s.Queries.ListMembers(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("ask/plan reminder: list members failed",
			"issue_id", util.UUIDToString(issue.ID), "error", err)
		return
	}
	notifType := "agent_question"
	detailsMap := map[string]any{"card_id": cardID, "reminder": true}
	title := "Reminder: the agent is still waiting for your answer"
	if isPlan {
		notifType = "plan_review"
		detailsMap = map[string]any{"plan_id": planID, "reminder": true}
		title = "Reminder: the agent is still waiting for your plan verdict"
	}
	details, err := json.Marshal(detailsMap)
	if err != nil {
		slog.Warn("ask/plan reminder: details encode failed", "error", err)
		return
	}
	detailsJSON := json.RawMessage(details)
	for _, m := range members {
		_, _, err := inbox.Deliver(ctx, s.Queries, s.Bus, inbox.Notification{Params: db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   issue.WorkspaceID,
			RecipientType: "member",
			RecipientID:   m.UserID,
			Type:          notifType,
			Severity:      "action_required",
			IssueID:       issue.ID,
			Title:         title,
			Body:          pgtype.Text{String: "waiting with no answer yet", Valid: true},
			ActorType:     pgtype.Text{String: "system", Valid: true},
			Details:       detailsJSON,
		}})
		if err != nil {
			slog.Warn("ask/plan reminder: inbox creation failed",
				"issue_id", util.UUIDToString(issue.ID), "error", err)
			continue
		}
	}
	slog.Info("parked ask/plan reminded",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"timeout_seconds", timeoutSeconds,
	)
}

// escalateAskPlanToLead posts the durable system comment and triggers the
// project lead (M-332). Every trigger here is best-effort: the comment is the
// record, and a logged failure never un-escalates anything.
//
// The comment carries NO mention:// links on purpose — the comment listener
// refuses mention parsing for system authors (cmd/server/notification_listeners.go),
// and a transcluded mention in a platform signal is exactly what that guard
// exists to stop. The lead is triggered programmatically below instead.
func (s *TaskService) escalateAskPlanToLead(ctx context.Context, task db.AgentTaskQueue, timeoutSeconds int, timeoutSource string) {
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("ask/plan escalation: load issue failed",
			"task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", err)
		return
	}

	// The project lead: member or agent (project.lead_type CHECK). A project
	// without a lead, no project at all, or an unreadable row degrades to the
	// comment alone — the contract names the lead, so an invented recipient is
	// worse than none.
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
			slog.Warn("ask/plan escalation: project gone, comment only",
				"issue_id", util.UUIDToString(issue.ID))
		case err != nil:
			slog.Warn("ask/plan escalation: load project failed, comment only",
				"issue_id", util.UUIDToString(issue.ID), "error", err)
		default:
			if project.LeadType.Valid && project.LeadID.Valid {
				leadType, leadID, leadLoaded = project.LeadType.String, project.LeadID, true
				leadName = s.askPlanLeadName(ctx, project.LeadType.String, project.LeadID)
			}
		}
	} else {
		slog.Warn("ask/plan escalation: issue has no project, comment only",
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
		Content:    renderAskPlanEscalationComment(task, timeoutSeconds, timeoutSource, leadType, leadName, leadLoaded, s.askPlanEscalationDecision(ctx, task)),
		Type:       "system",
		ParentID:   pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("ask/plan escalation: create system comment failed",
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
		slog.Warn("ask/plan escalation: no project lead set; comment posted without a trigger",
			"issue_id", util.UUIDToString(issue.ID))
		return
	}
	switch leadType {
	case "agent":
		// OriginDerived, not OriginNamed: nothing here is a person writing the
		// lead's name, so the Triage guard must still apply — a Triage issue
		// gets the comment and no task, guard not bypassed. Refusal (Triage,
		// archived, fail-closed attribution) is logged and dropped; the
		// comment stands and the card still waits for the Owner.
		if _, err := s.EnqueueTaskForMention(ctx, issue, leadID, comment.ID, OriginDerived); err != nil {
			slog.Warn("ask/plan escalation: lead agent task not enqueued",
				"issue_id", util.UUIDToString(issue.ID), "lead_agent_id", util.UUIDToString(leadID), "error", err)
		}
	case "member":
		details, err := json.Marshal(map[string]any{
			"task_id":         util.UUIDToString(task.ID),
			"timeout_seconds": timeoutSeconds,
			"source":          timeoutSource,
		})
		if err != nil {
			slog.Warn("ask/plan escalation: inbox details encode failed", "error", err)
			return
		}
		_, _, err = inbox.Deliver(ctx, s.Queries, s.Bus, inbox.Notification{Params: db.CreateInboxItemParams{
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
			Title:     "Ask/plan escalation: " + issue.Title,
			ActorType: pgtype.Text{String: "system", Valid: true},
			Details:   details,
		}})
		if err != nil {
			slog.Warn("ask/plan escalation: lead inbox creation failed",
				"issue_id", util.UUIDToString(issue.ID), "lead_user_id", util.UUIDToString(leadID), "error", err)
			return
		}
	default:
		slog.Warn("ask/plan escalation: unknown project lead type; comment only",
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

// askPlanEscalationDecision names the decision the lead is escalated to
// (M-500): the card's questions, or the plan's title. Best-effort — an
// unreadable row leaves it empty and the comment still says what waited.
func (s *TaskService) askPlanEscalationDecision(ctx context.Context, task db.AgentTaskQueue) string {
	if !task.TriggerEvidenceKind.Valid || !task.TriggerEvidenceRefID.Valid {
		return ""
	}
	switch task.TriggerEvidenceKind.String {
	case EvidenceQuestionAnswer:
		card, err := s.Queries.GetAgentQuestionCard(ctx, task.TriggerEvidenceRefID)
		if err != nil {
			return ""
		}
		var qs []askPlanQuestion
		if err := json.Unmarshal(card.Questions, &qs); err != nil || len(qs) == 0 {
			return ""
		}
		parts := make([]string, 0, len(qs))
		for i, q := range qs {
			parts = append(parts, fmt.Sprintf("Q%d: %s", i+1, strings.TrimSpace(q.Prompt)))
		}
		return strings.Join(parts, " | ")
	case EvidencePlanVerdict:
		plan, err := s.Queries.GetAgentPlan(ctx, task.TriggerEvidenceRefID)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("plan %q", plan.Title)
	default:
		return ""
	}
}

// renderAskPlanEscalationComment is the durable record and the lead's brief:
// what waited, how long, which setting governed it, that nothing was cancelled,
// the exact rule for answering on the Owner's behalf, and who it escalated to.
// Plain text only — no mention links (see escalateAskPlanToLead).
func renderAskPlanEscalationComment(task db.AgentTaskQueue, timeoutSeconds int, timeoutSource, leadType, leadName string, leadLoaded bool, decision string) string {
	var b strings.Builder
	isPlan := task.TriggerEvidenceKind.Valid && task.TriggerEvidenceKind.String == EvidencePlanVerdict
	if isPlan {
		fmt.Fprintf(&b, "**Ask/plan escalation:** the plan on this run has waited %ds for a verdict — the full effective timeout (%s). Nothing was cancelled or withdrawn: the plan stays in review, the run stays parked, and the Owner's verdict still resumes it.\n", timeoutSeconds, timeoutSource)
		b.WriteString("Project lead: a plan verdict stays the Owner's alone — there is no lead verdict path, so this plan waits.\n")
	} else {
		fmt.Fprintf(&b, "**Ask/plan escalation:** the question card on this run has waited %ds for an answer — the full effective timeout (%s). Nothing was withdrawn or cancelled: the card stays open, the run stays parked, and the first answer resumes it.\n", timeoutSeconds, timeoutSource)
		b.WriteString("Project lead: answer it on the Owner's behalf **only** if the decision is covered by the approved plan or the Owner's recorded decisions, or if it is reversible — with `multica question-card answer <card-id> --option \"<label>\"` (or `--text` for a free-text answer). Otherwise do not answer: leaving the card for the Owner is a valid outcome — say why in one reply and move on.\n")
	}
	if decision != "" {
		fmt.Fprintf(&b, "Decision waiting: %s\n", decision)
	}
	switch {
	case !leadLoaded:
		b.WriteString("No project lead is set on this project, so nothing was triggered beyond this note. It waits for the Owner.")
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
