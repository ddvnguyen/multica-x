package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type decisionResponseRequest struct {
	ClientRequestID       string `json:"client_request_id"`
	ExpectedIssueRevision *int64 `json:"expected_issue_revision"`
	Decision              string `json:"decision"`
	Rationale             string `json:"rationale"`
	NextStatus            string `json:"next_status"`
}

func (h *Handler) DecisionResponse(w http.ResponseWriter, r *http.Request) {
	r = h.withWakeupActor(r)
	ctx := r.Context()
	issueID := chi.URLParam(r, "issueId")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID := requestUserID(r)
	workspaceID := issue.WorkspaceID
	authorType, authorID := h.resolveActor(r, userID, uuidToString(workspaceID))
	if authorType != "member" {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	authorUUID, err := util.ParseUUID(authorID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid actor")
		return
	}

	var req decisionResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Decision) == "" {
		writeError(w, http.StatusBadRequest, "decision is required")
		return
	}
	if strings.TrimSpace(req.NextStatus) == "" {
		writeError(w, http.StatusBadRequest, "next_status is required")
		return
	}
	if req.ExpectedIssueRevision == nil || *req.ExpectedIssueRevision < 1 {
		writeError(w, http.StatusBadRequest, "expected_issue_revision is required")
		return
	}
	clientRequestID, ok := parseUUIDOrBadRequest(w, req.ClientRequestID, "client_request_id")
	if !ok {
		return
	}

	// Matching retry must return the original receipt before any mutable
	// workflow/agent preparation or authorization changes.
	if existing, err := h.Queries.GetIssueDecisionResponseByRequest(ctx, db.GetIssueDecisionResponseByRequestParams{
		WorkspaceID:     workspaceID,
		ClientRequestID: clientRequestID,
	}); err == nil {
		if uuidToString(existing.IssueID) != uuidToString(issue.ID) ||
			uuidToString(existing.ActorID) != authorID ||
			existing.Decision != req.Decision ||
			existing.Rationale != req.Rationale ||
			existing.NextStatus != req.NextStatus ||
			existing.SourceRevision != *req.ExpectedIssueRevision {
			writeError(w, http.StatusConflict, "client_request_id is already in use")
			return
		}
		writeDecisionResponseReceipt(w, existing)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check existing response")
		return
	}

	def, project, err := issueworkflow.ForProject(ctx, h.Queries, workspaceID, issue.ProjectID)
	if err != nil || def == nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow")
		return
	}
	if err := issueworkflow.CheckStatus(def, req.NextStatus); err != nil {
		writeError(w, http.StatusBadRequest, "invalid next_status")
		return
	}

	// Prepare network-dependent handoff data before locking. The target is
	// revalidated in the transaction.
	var prepared service.PreparedIssueHandoff
	hasPrepared := false
	var preparedSquadID pgtype.UUID
	var preparedKind string // "agent" or "squad"
	step, hasStep := def.Step(req.NextStatus)
	if hasStep && step.HandsOff() {
		assigneeType, assigneeID, ok := issueworkflow.ResolveHandler(step, project, issue.CreatorType, issue.CreatorID)
		if ok {
			switch assigneeType {
			case "agent":
				p, err := h.TaskService.PrepareIssueHandoff(ctx, issue, assigneeID, authorUUID)
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				prepared = p
				hasPrepared = true
				preparedKind = "agent"
			case "squad":
				squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: assigneeID, WorkspaceID: workspaceID})
				if err != nil {
					writeError(w, http.StatusBadRequest, "invalid handoff squad")
					return
				}
				p, err := h.TaskService.PrepareIssueHandoff(ctx, issue, squad.LeaderID, authorUUID)
				if err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				prepared = p
				hasPrepared = true
				preparedKind = "squad"
				preparedSquadID = assigneeID
			case "project_lead", "creator":
				// May resolve to an agent-type principal handled above via its
				// type; if the resolved type is agent/squad it would already
				// have matched. Member/creator-human destinations need no task.
			}
			_ = assigneeID
		}
	}

	tx, err := h.beginWakeupWrite(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := h.Queries.WithTx(tx)

	lockedIssue, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
		ID:          issue.ID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock issue")
		return
	}

	// Matching retry: return original receipt before fresh-state handling.
	if existing, err := qtx.GetIssueDecisionResponseByRequest(ctx, db.GetIssueDecisionResponseByRequestParams{
		WorkspaceID:     workspaceID,
		ClientRequestID: clientRequestID,
	}); err == nil {
		if uuidToString(existing.IssueID) != uuidToString(lockedIssue.ID) ||
			uuidToString(existing.ActorID) != authorID ||
			existing.Decision != req.Decision ||
			existing.Rationale != req.Rationale ||
			existing.NextStatus != req.NextStatus ||
			existing.SourceRevision != *req.ExpectedIssueRevision {
			writeError(w, http.StatusConflict, "client_request_id is already in use")
			return
		}
		committed = true
		_ = tx.Rollback(ctx)
		writeDecisionResponseReceipt(w, existing)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check existing response")
		return
	}

	// Expected revision fence.
	if lockedIssue.Revision != *req.ExpectedIssueRevision {
		writeRevisionConflict(w, "issue", lockedIssue.ID, *req.ExpectedIssueRevision, lockedIssue.Revision)
		return
	}
	if lockedIssue.Status != "wait_for_decision" {
		writeError(w, http.StatusConflict, "issue is not waiting for a decision")
		return
	}

	// Transaction-scoped membership check.
	if _, err := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		WorkspaceID: workspaceID, UserID: authorUUID,
	}); err != nil {
		writeError(w, http.StatusForbidden, "not a member of this workspace")
		return
	}

	// Revalidate workflow under the transaction.
	defTx, _, err := issueworkflow.ForProject(ctx, qtx, workspaceID, lockedIssue.ProjectID)
	if err != nil || defTx == nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow")
		return
	}
	if err := issueworkflow.CheckStatus(defTx, req.NextStatus); err != nil {
		writeError(w, http.StatusBadRequest, "invalid next_status")
		return
	}

	params := db.UpdateIssueParams{
		SourceTaskID:     h.wakeupSourceTaskID(r),
		ID:               lockedIssue.ID,
		AssigneeType:     lockedIssue.AssigneeType,
		AssigneeID:       lockedIssue.AssigneeID,
		StartDate:        lockedIssue.StartDate,
		DueDate:          lockedIssue.DueDate,
		ParentIssueID:    lockedIssue.ParentIssueID,
		ProjectID:        lockedIssue.ProjectID,
		Stage:            lockedIssue.Stage,
		ExpectedRevision: pgtype.Int8{Int64: lockedIssue.Revision, Valid: true},
	}
	params.Status = pgtype.Text{String: req.NextStatus, Valid: true}

	handoff, err := h.applyIssueWorkflow(ctx, qtx, lockedIssue, &params, true, false, false, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Verify prepared handoff still matches the transaction-resolved target.
	// Only agent/squad destinations require a durable task; member and other
	// human destinations commit receipt/comment/status without one.
	requiresTask := handoff != nil && params.AssigneeType.Valid &&
		(params.AssigneeType.String == "agent" || params.AssigneeType.String == "squad")
	var task db.AgentTaskQueue
	hasTask := false
	if requiresTask {
		if !hasPrepared {
			writeError(w, http.StatusConflict, "handoff target changed during transaction")
			return
		}
		if handoff == nil {
			writeError(w, http.StatusConflict, "handoff target changed during transaction")
			return
		}
		switch preparedKind {
		case "agent":
			if params.AssigneeType.String != "agent" || !params.AssigneeID.Valid || params.AssigneeID != prepared.HandoffAgent().ID {
				writeError(w, http.StatusConflict, "handoff target changed during transaction")
				return
			}
		case "squad":
			if params.AssigneeType.String != "squad" || !params.AssigneeID.Valid || params.AssigneeID != preparedSquadID {
				writeError(w, http.StatusConflict, "handoff target changed during transaction")
				return
			}
		}
	} else if hasPrepared {
		writeError(w, http.StatusConflict, "handoff target changed during transaction")
		return
	}

	// Transaction-scoped authorization + update before enqueue so the queued
	// task is stamped with the destination workflow step.
	if requiresTask {
		switch preparedKind {
		case "agent":
			agent, err := qtx.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: prepared.HandoffAgent().ID, WorkspaceID: workspaceID})
			if err != nil || agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
				writeError(w, http.StatusConflict, "handoff agent changed during preparation")
				return
			}
			if !h.canInvokeAgent(ctx, agent, authorType, authorID, authorID, uuidToString(workspaceID)) {
				writeError(w, http.StatusForbidden, "invocation_not_allowed")
				return
			}
		case "squad":
			squad, err := qtx.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: preparedSquadID, WorkspaceID: workspaceID})
			if err != nil {
				writeError(w, http.StatusForbidden, "invalid handoff squad")
				return
			}
			leader, err := qtx.GetAgent(ctx, prepared.HandoffAgent().ID)
			if err != nil || leader.ArchivedAt.Valid || !leader.RuntimeID.Valid || leader.ID != squad.LeaderID {
				writeError(w, http.StatusConflict, "handoff agent changed during preparation")
				return
			}
			if !h.canInvokeAgent(ctx, leader, authorType, authorID, authorID, uuidToString(workspaceID)) {
				writeError(w, http.StatusForbidden, "invocation_not_allowed")
				return
			}
		}
	}

	// Update issue status/assignee first so task workflow_step is destination.
	updatedIssue, cancelled, err := updateIssueStoppingWakeups(ctx, qtx, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update issue")
		return
	}

	if requiresTask {
		t, err := h.TaskService.EnqueuePreparedIssueHandoffInTx(ctx, qtx, updatedIssue, preparedSquadID, prepared)
		if err != nil {
			if errors.Is(err, service.ErrDuplicatePendingTask) {
				writeError(w, http.StatusConflict, "duplicate pending task")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		task = t
		hasTask = true
	}

	// Ordinary timeline comment.
	content := req.Decision
	if req.Rationale != "" {
		content += "\n\n" + req.Rationale
	}
	commentRow, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     lockedIssue.ID,
		WorkspaceID: workspaceID,
		AuthorType:  "member",
		AuthorID:    authorUUID,
		Content:     content,
		Type:        "comment",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}

	// Receipt.
	receiptID := dbid.NewV7()
	taskID := pgtype.UUID{Valid: false}
	if hasTask {
		taskID = task.ID
	}
	receipt, err := qtx.CreateIssueDecisionResponse(ctx, db.CreateIssueDecisionResponseParams{
		ID:              receiptID,
		WorkspaceID:     workspaceID,
		IssueID:         lockedIssue.ID,
		ClientRequestID: clientRequestID,
		ActorID:         authorUUID,
		SourceRevision:  lockedIssue.Revision,
		Decision:        req.Decision,
		Rationale:       req.Rationale,
		NextStatus:      req.NextStatus,
		CommentID:       commentRow.ID,
		TaskID:          taskID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create receipt")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit")
		return
	}
	committed = true

	// Post-commit side effects only.
	if hasTask {
		h.TaskService.FinalizeIssueHandoff(ctx, task, prepared)
	}
	h.broadcastCancelledWakeups(ctx, workspaceID, cancelled)
	h.publish(protocol.EventIssueUpdated, uuidToString(workspaceID), "member", authorID, map[string]any{
		"issue":          issueToResponse(updatedIssue, h.getIssuePrefix(ctx, workspaceID)),
		"status_changed": true,
	})
	comment := db.Comment{
		ID: commentRow.ID, IssueID: commentRow.IssueID, AuthorType: commentRow.AuthorType,
		AuthorID: commentRow.AuthorID, Content: commentRow.Content, Type: commentRow.Type,
		CreatedAt: commentRow.CreatedAt, UpdatedAt: commentRow.UpdatedAt, ParentID: commentRow.ParentID,
		WorkspaceID: commentRow.WorkspaceID, ResolvedAt: commentRow.ResolvedAt,
		ResolvedByType: commentRow.ResolvedByType, ResolvedByID: commentRow.ResolvedByID,
		SourceTaskID: commentRow.SourceTaskID, QuickActionID: commentRow.QuickActionID,
		ViaPluginID: commentRow.ViaPluginID, Revision: commentRow.Revision,
		RecoverySettledAt: commentRow.RecoverySettledAt, DeletedAt: commentRow.DeletedAt,
		SuppressedAgentIds: commentRow.SuppressedAgentIds,
	}
	respComment := commentToResponse(comment, nil, nil)
	respComment.IssueRevision = commentRow.IssueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(workspaceID), "member", authorID, map[string]any{
		"comment":        respComment,
		"issue_title":    issue.Title,
		"issue_revision": commentRow.IssueRevision,
	})

	writeDecisionResponseReceipt(w, receipt)
}

func writeDecisionResponseReceipt(w http.ResponseWriter, receipt db.IssueDecisionResponse) {
	taskID := ""
	if receipt.TaskID.Valid {
		taskID = uuidToString(receipt.TaskID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                uuidToString(receipt.ID),
		"workspace_id":      uuidToString(receipt.WorkspaceID),
		"issue_id":          uuidToString(receipt.IssueID),
		"client_request_id": uuidToString(receipt.ClientRequestID),
		"actor_id":          uuidToString(receipt.ActorID),
		"source_revision":   receipt.SourceRevision,
		"decision":          receipt.Decision,
		"rationale":         receipt.Rationale,
		"next_status":       receipt.NextStatus,
		"comment_id":        uuidToString(receipt.CommentID),
		"task_id":           taskID,
		"created_at":        receipt.CreatedAt,
	})
}
