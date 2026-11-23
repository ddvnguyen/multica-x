package handler

// Daemon-side plan gate (docs/plannotator-gate.md).
//
// Two routes whose only caller is the daemon that parked a run on a plan:
//
//	GET  /api/daemon/tasks/{taskId}/parked-plan     the open plan's text
//	POST /api/daemon/tasks/{taskId}/plan-verdict    the gate's verdict
//
// They exist because the member-facing set cannot serve this caller. The plan
// page reads by issue id and verdicts with a member session; a daemon has a
// task token and no user. Both routes therefore resolve the plan FROM THE TASK:
// a daemon can reach exactly one plan — the open one on its own task's issue —
// and can neither name nor guess another. The verdict still runs through
// TaskService.ReviewPlan, so the revision fence, the first-verdict-wins CAS and
// the resume enqueue are the ones the panel already uses; this file adds no
// review path of its own and stores nothing.
//
// Provenance: there is no member behind a gate verdict, so reviewed_by stays
// NULL and the note names the gate. An approved plan with no approver is the
// honest record — an external annotator is not a workspace member, and
// inventing one would put a person's name on a decision they did not make.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// daemonPlanGateNote is recorded as every gate verdict's note. It is the only
// place the provenance survives: reviewed_by is NULL, so a reader seeing an
// approved plan without an approver finds this line and knows why.
const daemonPlanGateNote = "Verdict from the Plannotator gate on the daemon host"

// daemonParkedPlanMaxBytes bounds the text handed to the gate. A plan's
// sections are already bounded by the plan limits; this is the belt against a
// runaway rows payload being written to a temp file and fed to a process.
const daemonParkedPlanMaxBytes = 262144

// daemonParkedPlanResponse is GET's answer. Status is "none" when the task has
// no open plan in review — the common case for an ask park, and for a plan a
// human already verdict'd.
type daemonParkedPlanResponse struct {
	Status            string `json:"status"`
	PlanID            string `json:"plan_id,omitempty"`
	Title             string `json:"title,omitempty"`
	Revision          int32  `json:"revision,omitempty"`
	SubmittedRevision int32  `json:"submitted_revision,omitempty"`
	Markdown          string `json:"markdown,omitempty"`
}

// daemonPlanVerdictRequest is POST's body.
type daemonPlanVerdictRequest struct {
	PlanID       string `json:"plan_id"`
	BaseRevision int32  `json:"base_revision"`
	// Verdict is the plan verdict vocabulary, not the gate's: the daemon
	// translates approved/annotated into approve/changes_requested.
	Verdict string `json:"verdict"`
	// Feedback is the annotator's Markdown, carried as the review's comments.
	Feedback string `json:"feedback,omitempty"`
}

// GetParkedPlan serves the open plan on the task's issue to the task's daemon.
func (h *Handler) GetParkedPlan(w http.ResponseWriter, r *http.Request) {
	task, _, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	plan, ok := h.openPlanForTask(w, r, task)
	if !ok {
		return
	}
	if plan.ID == (pgtype.UUID{}) {
		writeJSON(w, http.StatusOK, daemonParkedPlanResponse{Status: "none"})
		return
	}
	markdown := parkedPlanMarkdown(plan.Title, plan.Sections)
	if markdown == "" {
		// A plan submitted with no body is nothing to review. Say so plainly
		// rather than handing the gate an empty file and recording
		// "changes_requested: " with no text behind it.
		writeJSON(w, http.StatusOK, daemonParkedPlanResponse{Status: "none"})
		return
	}
	if len(markdown) > daemonParkedPlanMaxBytes {
		markdown = markdown[:daemonParkedPlanMaxBytes]
	}
	writeJSON(w, http.StatusOK, daemonParkedPlanResponse{
		Status:            "ready",
		PlanID:            uuidToString(plan.ID),
		Title:             plan.Title,
		Revision:          plan.Revision,
		SubmittedRevision: plan.SubmittedRevision.Int32,
		Markdown:          markdown,
	})
}

// SubmitParkedPlanVerdict applies a gate's verdict to the task's open plan.
func (h *Handler) SubmitParkedPlanVerdict(w http.ResponseWriter, r *http.Request) {
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	// The workspace id comes from the daemon scope the access check resolved,
	// not from the request: a sqlc round-trip, so parsing it is safe.
	workspaceUUID := parseUUID(workspaceID)
	var req daemonPlanVerdictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Verdict != "approve" && req.Verdict != "changes_requested" && req.Verdict != "cancel" {
		writeError(w, http.StatusBadRequest, "verdict must be approve, changes_requested or cancel")
		return
	}
	requestedPlanID, ok := parseUUIDOrBadRequest(w, req.PlanID, "plan_id")
	if !ok {
		return
	}
	if len(req.Feedback) > planCommentMax {
		writeError(w, http.StatusBadRequest, "feedback is too long")
		return
	}
	plan, ok := h.openPlanForTask(w, r, task)
	if !ok {
		return
	}
	if plan.ID == (pgtype.UUID{}) {
		// The plan left in_review between the gate's read and its verdict: a
		// human decided it, or the run was cancelled. Not an error the gate
		// can act on, and not one it may retry.
		writeErrorCode(w, http.StatusConflict, "plan_not_in_review", "the plan is not awaiting a verdict")
		return
	}
	if plan.ID != requestedPlanID {
		// The gate reviewed a different plan than the one now open (a
		// resubmission replaced it). Its feedback belongs to text that no
		// longer exists, so it must not land on the new revision.
		writeErrorCode(w, http.StatusConflict, "stale_plan_revision", "the open plan changed while the gate was reviewing")
		return
	}
	comments := []string{}
	if feedback := strings.TrimSpace(req.Feedback); feedback != "" {
		comments = append(comments, feedback)
	}
	commentsJSON, err := json.Marshal(comments)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode comments")
		return
	}
	// reviewed_by is NULL on purpose: no member made this call. See
	// daemonPlanGateNote.
	reviewedBy := pgtype.UUID{}
	reviewed, resumeTask, promoted, err := h.TaskService.ReviewPlan(
		r.Context(), plan.ID, workspaceUUID, reviewedBy, req.BaseRevision, req.Verdict, commentsJSON, daemonPlanGateNote)
	switch {
	case errors.Is(err, service.ErrPlanClosed):
		writeErrorCode(w, http.StatusConflict, "plan_closed", "the plan is final and no longer changes")
		return
	case errors.Is(err, service.ErrPlanNotInReview):
		writeErrorCode(w, http.StatusConflict, "plan_not_in_review", "the plan is not awaiting a verdict")
		return
	case errors.Is(err, service.ErrPlanStaleRevision):
		body := map[string]any{"error": "stale_plan_revision", "current_revision": plan.Revision}
		writeJSON(w, http.StatusConflict, body)
		return
	case errors.Is(err, service.ErrPlanNeedsComment):
		writeErrorCode(w, http.StatusBadRequest, "review_needs_comment", "changes_requested needs at least one comment or a note")
		return
	case errors.Is(err, service.ErrIssueRevisionConflict):
		writeErrorCode(w, http.StatusConflict, "issue_revision_conflict", "the issue changed while reviewing; retry the verdict")
		return
	case errors.Is(err, service.ErrDuplicatePendingTask):
		// The verdict was recorded; a queued run already carries it. Resume is
		// the one thing that matters and it is happening, so this is a success
		// with a caveat rather than a failure the gate should retry.
		writeErrorCode(w, http.StatusConflict, "run_already_queued", "the verdict was recorded but a run is already queued for this issue")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to record the verdict")
		return
	}
	if promoted != nil {
		prefix := ""
		if ws, wsErr := h.Queries.GetWorkspace(r.Context(), workspaceUUID); wsErr == nil {
			prefix = issuePrefixForWorkspace(ws)
		}
		h.publish(protocol.EventIssueUpdated, workspaceID, "system", "", map[string]any{
			"issue":               issueToResponse(*promoted, prefix),
			"description_changed": true,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":        planToResponse(reviewed),
		"resume_task": uuidToString(resumeTask.ID),
	})
}

// openPlanForTask resolves the task's open plan. It answers ok=false only when
// it has already written an error response; a task with no open plan returns
// the zero plan and ok=true, because that is a normal answer, not a failure.
func (h *Handler) openPlanForTask(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue) (db.AgentPlan, bool) {
	if !task.IssueID.Valid {
		return db.AgentPlan{}, true
	}
	plan, err := h.Queries.GetOpenAgentPlanByIssue(r.Context(), task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentPlan{}, true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load the open plan")
		return db.AgentPlan{}, false
	}
	// Only a submitted plan awaiting a verdict is reviewable. An open draft is
	// the agent still writing, and a final plan has already been decided.
	if plan.Status != "in_review" || !plan.SubmittedRevision.Valid {
		return db.AgentPlan{}, true
	}
	return plan, true
}

// parkedPlanMarkdown renders a plan as the Markdown a reviewer annotates:
// the title, then each section's heading and body in outline order.
//
// It is deliberately not the plan page's renderer. The page has the issue, the
// comment threads and the revision selector around it; the gate gets a
// standalone document, so the text has to stand on its own.
func parkedPlanMarkdown(title string, sectionsJSON []byte) string {
	var b strings.Builder
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		fmt.Fprintf(&b, "# %s\n\n", trimmed)
	}
	var sections []planSectionJSON
	if err := json.Unmarshal(sectionsJSON, &sections); err == nil {
		for _, section := range sections {
			if heading := strings.TrimSpace(section.Heading); heading != "" {
				fmt.Fprintf(&b, "## %s\n\n", heading)
			}
			if body := strings.TrimSpace(section.Markdown); body != "" {
				b.WriteString(body)
				b.WriteString("\n\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}
