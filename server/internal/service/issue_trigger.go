package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RunEnqueueSource identifies which kind of issue write would start an agent
// run. It is surfaced in preview responses so the UI can explain each trigger.
type RunEnqueueSource string

const (
	// RunSourceAssign covers issue creation and assignee changes — the issue
	// is being handed to an agent/squad. Parks silently on backlog.
	RunSourceAssign RunEnqueueSource = "assign"
	// RunSourceStatus covers promoting an already-assigned issue out of
	// backlog into an active status.
	RunSourceStatus RunEnqueueSource = "status"
)

// IssueTriggerProbe carries the request-scoped checks WillEnqueueRun cannot
// resolve from issue state alone.
//
// CanAccessAgent is the private-agent gate. The write paths enforce it at the
// HTTP boundary (validateAssigneePair on assign, canEnqueueSquadLeader inside
// the squad enqueue helper) and therefore pass an allow-all probe so the gate
// is never duplicated or sunk into the service layer. Preview passes the real
// gate so it never leaks a private agent's readiness to a member who cannot
// see it. A nil func is treated as allow-all.
//
// IsSelfLoop reports whether promoting this issue out of backlog would be the
// calling agent re-triggering its own running task for the target agent
// (the assignee or squad leader). Only the status source consults it, after
// resolving that target. A nil func means "not a self-loop".
//
// SuppressActiveSelfAssignment reports whether a direct agent assignment is a
// trusted agent claiming ownership for itself while the target (issue, agent)
// pair already has a non-terminal task. The ownership update still succeeds;
// only the duplicate enqueue is suppressed. Cross-issue handoffs to a fresh
// target remain runnable. A nil func means "do not suppress".
//
// RoleRequester is the human a Role pick is authorized against (M-597): the
// acting member, or the resolved originator when an agent or system principal
// acts. A Role assignee is declined when the Role's own access rules refuse that
// human, so preview and the write path report the verdict the enqueue reaches
// instead of promising a run the enqueue refuses. A zero UUID declines every
// Role: nobody can be authorised.
type IssueTriggerProbe struct {
	CanAccessAgent               func(agent db.Agent) bool
	IsSelfLoop                   func(targetAgentID pgtype.UUID) bool
	SuppressActiveSelfAssignment func(agentID pgtype.UUID) bool
	RoleRequester                pgtype.UUID
}

// IssueTriggerInput describes one prospective issue write in its post-write
// shape. AssigneeChanged / StatusChanged mark which fields the write touches;
// IsCreate marks a brand-new issue (no prior task to cancel, no self-loop).
type IssueTriggerInput struct {
	Issue           db.Issue
	PrevStatus      string
	IsCreate        bool
	AssigneeChanged bool
	StatusChanged   bool
	// EnteredHandoffStep marks a write that moved the issue into a project
	// workflow column with a handler (MUL-7420). The step is new work even
	// when its handler is the agent whose run made the move, so the
	// self-assignment suppression does not apply.
	EnteredHandoffStep bool
}

// IssueRunTrigger is the resolved decision shared by preview and the write
// paths. AgentID is the agent that will actually run — the assignee for an
// agent issue, the squad leader for a squad issue.
//
// DeclineReason is set only when the write could have started a run (a create,
// an assignment, or a promotion out of backlog) and does not. The write paths
// log it so a run that silently did not start can be explained (#10845).
// Writes that never start a run leave it empty.
type IssueRunTrigger struct {
	IssueID         pgtype.UUID
	AgentID         pgtype.UUID
	AssigneeType    string
	Source          RunEnqueueSource
	WorkflowHandoff bool
	// WasCorrection marks a run that starts from a correction (M-604): the
	// issue moved backwards (Needs-changes back-step or sent back from
	// review to a working step). The dispatcher stamps it into the task's
	// context so the claim can render the mmx inner-loop line.
	WasCorrection bool
	DeclineReason dispatch.ReasonCode
}

// roleRunForTrigger is the WillEnqueueRun form of the M-499 slice 2 Role
// exemption: the assign/create/status trigger carries no RuntimeLookup, so
// flags come from the TaskService. A nil service or nil flags fails closed,
// keeping a flag-off Role-shaped row on the pre-M-510 Agent path.
func roleRunForTrigger(ctx context.Context, s *IssueService, agent db.Agent) bool {
	if s == nil || s.TaskService == nil {
		return false
	}
	return roleRunEnabledWithFlags(ctx, s.TaskService.FeatureFlags, agent)
}

func declineRun(reason dispatch.ReasonCode) (IssueRunTrigger, bool) {
	return IssueRunTrigger{DeclineReason: reason}, false
}

func allowAllAgents(db.Agent) bool { return true }

// WillEnqueueRun is the single predicate answering "will this issue write
// start an agent run, and for whom". It is the one source of truth shared by
// the issue update / batch-update write paths and the preview endpoint,
// replacing the per-site copies that drifted (squad omitted, self-loop
// omitted, four entry points inconsistent — see MUL-3375).
//
// It is intentionally a distinct predicate from the comment trigger
// (assignee fallback comment routing): issue writes park on backlog while comments fire
// in any status. The two only share leaf readiness checks (AgentReadiness,
// the pending-task dedup), not the top-level decision.
//
// The decision must equal the real enqueue conditions so preview never claims
// a net-new run that the write path then drops. The write enqueues through
// CreateAgentTask, guarded by the (issue_id, agent_id) partial unique index
// over pending (queued/dispatched) tasks; the pending check below mirrors that
// guard, and only the status source needs it:
//   - status source (backlog → active) can re-fire against an assignee that
//     already holds a pending task (e.g. one a @mention raised while the issue
//     sat in backlog); the check keeps preview from promising a run the unique
//     index would coalesce away.
//   - assign source (create / assignee change) skips the check: a create
//     targets a fresh issue with no prior task, and a reassignment no longer
//     cancels existing tasks (#4963 / MUL-4113) — in the rare case the new
//     assignee already holds a pending task the insert simply no-ops on the
//     same unique index, so the assignee still ends up with one pending run.
func (s *IssueService) WillEnqueueRun(ctx context.Context, in IssueTriggerInput, probe IssueTriggerProbe) (IssueRunTrigger, bool) {
	issue := in.Issue
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return IssueRunTrigger{}, false
	}
	if issue.AssigneeType.String != "agent" && issue.AssigneeType.String != "squad" {
		return IssueRunTrigger{}, false
	}
	canAccess := probe.CanAccessAgent
	if canAccess == nil {
		canAccess = allowAllAgents
	}

	// Only the fixed backlog key parks work. Leaving it for another unstarted
	// or started status can enqueue a run; custom statuses do not inherit parking.
	// Effective preserves built-in behavior and resolves custom terminal categories
	// so moving to done/closed cannot start work.
	currentStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)
	prevStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, in.PrevStatus)

	var source RunEnqueueSource
	switch {
	case in.IsCreate || in.AssigneeChanged:
		source = RunSourceAssign
	case in.StatusChanged && prevStatus == "backlog" &&
		currentStatus != "backlog" &&
		currentStatus != "done" && currentStatus != "cancelled":
		source = RunSourceStatus
	default:
		return IssueRunTrigger{}, false
	}

	// Triage is stricter than the backlog parking lot: backlog defers a run,
	// Triage refuses one outright (MUL-7189 §2.3). Deciding it here is what
	// keeps the trigger PREVIEW honest — the queue door would refuse the insert
	// either way, but silently, and the preview would have promised a run.
	//
	// Leaving Triage needs no case of its own. Triage is not a status, so accept
	// clears this field and then takes the ordinary create / assign path; there
	// is no "was in triage" transition for this predicate to recognise.
	if issue.TriageState.Valid {
		return declineRun(dispatch.ReasonIssueInTriage)
	}
	// Backlog is the parking lot: assigning into backlog never starts a run.
	if source == RunSourceAssign && currentStatus == "backlog" {
		return declineRun(dispatch.ReasonDeferred)
	}

	switch issue.AssigneeType.String {
	case "agent":
		agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
		if err != nil || agent.ArchivedAt.Valid {
			return declineRun(dispatch.ReasonTargetUnavailable)
		}
		if !agent.RuntimeID.Valid && !roleRunForTrigger(ctx, s, agent) {
			return declineRun(dispatch.ReasonAgentRuntimeRequired)
		}
		if !canAccess(agent) {
			return declineRun(dispatch.ReasonInvocationNotAllowed)
		}
		// A Role has its own access rules and no runtime of its own; the enqueue
		// authorizes it with the same predicate, so both sides agree (M-597).
		if roleRunForTrigger(ctx, s, agent) && s.TaskService.authorizeRoleForRun(ctx, agent, issue.WorkspaceID, probe.RoleRequester) != nil {
			return declineRun(dispatch.ReasonInvocationNotAllowed)
		}
		if source == RunSourceStatus && probe.IsSelfLoop != nil && probe.IsSelfLoop(issue.AssigneeID) {
			return declineRun(dispatch.ReasonAlreadyActive)
		}
		if source == RunSourceAssign && !in.IsCreate && !in.EnteredHandoffStep && probe.SuppressActiveSelfAssignment != nil &&
			probe.SuppressActiveSelfAssignment(issue.AssigneeID) {
			return declineRun(dispatch.ReasonAlreadyActive)
		}
		// A step entered under the agent's own run queues behind it; one
		// already queued covers the new step too.
		if (source == RunSourceStatus || in.EnteredHandoffStep) && s.hasPendingRun(ctx, issue.ID, issue.AssigneeID) {
			return declineRun(dispatch.ReasonCoalesced)
		}
		return IssueRunTrigger{
			IssueID:      issue.ID,
			AgentID:      issue.AssigneeID,
			AssigneeType: "agent",
			Source:       source,
			// M-604: true when this write moved the card backwards (the
			// predicate self-gates: creates and forward moves return false).
			WasCorrection: s.TaskService.isBackStepCorrection(ctx, in.PrevStatus, issue),
		}, true

	case "squad":
		// Pair-scoped self-assignment suppression intentionally applies only to
		// direct agent ownership. Assigning a squad changes execution context
		// (leader briefing, roles, and member routing), so even when the acting
		// agent is that squad's leader it is an intentional group handoff rather
		// than a redundant direct self-claim. The status path below still uses
		// the leader's self-loop and pending-task guards.
		squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return declineRun(dispatch.ReasonTargetUnavailable)
		}
		leader, err := s.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil {
			return declineRun(dispatch.ReasonTargetUnavailable)
		}
		verdict, err := AgentReadiness(ctx, s.runtimeLookup(s.Queries), leader)
		if err != nil {
			return declineRun(dispatch.ReasonInternalError)
		}
		if !verdict.Ready() {
			return declineRun(verdict.Reason)
		}
		if !canAccess(leader) {
			return declineRun(dispatch.ReasonInvocationNotAllowed)
		}
		if source == RunSourceStatus && probe.IsSelfLoop != nil && probe.IsSelfLoop(squad.LeaderID) {
			return declineRun(dispatch.ReasonAlreadyActive)
		}
		if source == RunSourceStatus && s.hasPendingRun(ctx, issue.ID, squad.LeaderID) {
			return declineRun(dispatch.ReasonCoalesced)
		}
		return IssueRunTrigger{
			IssueID:         issue.ID,
			AgentID:         squad.LeaderID,
			AssigneeType:    "squad",
			Source:          source,
			WorkflowHandoff: in.EnteredHandoffStep,
			WasCorrection:   s.TaskService.isBackStepCorrection(ctx, in.PrevStatus, issue),
		}, true
	}
	return IssueRunTrigger{}, false
}

// hasPendingRun reports whether the agent already holds a queued or dispatched
// task for the issue (the (issue_id, agent_id) unique-index slot). Errors fail
// closed to "pending" so preview never over-promises a run.
func (s *IssueService) hasPendingRun(ctx context.Context, issueID, agentID pgtype.UUID) bool {
	pending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
		// Key dedup on the reviewed head so a pending run against an old HEAD
		// does not shadow a request after HEAD advanced (TEN-356).
		HeadSha: headShaText(s.TaskService.ResolveIssueReviewSHA(ctx, issueID)),
	})
	if err != nil {
		return true
	}
	return pending
}

// correctionReviewSteps are the review-stage statuses a card is sent back
// FROM (M-604, Architect decision on plan v1): a member moving the card out
// of one of these into a working step is correcting the agent's work.
var correctionReviewSteps = map[string]bool{"in_review": true, "agent_review_deploy": true}

// correctionTerminalSteps are never "working steps": landing here ends the
// card instead of asking for another pass.
var correctionTerminalSteps = map[string]bool{"done": true, "cancelled": true}

// isBackStepCorrection reports whether a status write moved the card
// backwards (M-604 trigger 3): either the exact Needs-changes back-step of
// the project's workflow, or a send-back from a review step to a working
// step. prevStatus is the status before the write, issue the post-write row.
// No general step ordering exists, so only these two pinned shapes count.
// A TaskService method so both the issue-write predicate (via IssueService)
// and the comment-enqueue check share it.
func (s *TaskService) isBackStepCorrection(ctx context.Context, prevStatus string, issue db.Issue) bool {
	if prevStatus == "" || prevStatus == issue.Status {
		return false
	}
	if correctionReviewSteps[prevStatus] && !correctionTerminalSteps[issue.Status] {
		return true
	}
	if s == nil || s.Queries == nil {
		return false
	}
	def, _, err := issueworkflow.ForProject(ctx, s.Queries, issue.WorkspaceID, issue.ProjectID)
	if err != nil || def == nil {
		return false
	}
	if step, ok := def.Step(prevStatus); ok && step.BackStatusKey != "" && step.BackStatusKey == issue.Status {
		return true
	}
	return false
}
