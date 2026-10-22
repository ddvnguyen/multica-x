package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
)

// Completion is a human decision.
//
// An issue moves into the done category when a person reads it and agrees the
// work is finished. A machine cannot make that call: an agent run presents a
// task token (X-Actor-Source: task_token) and a cloud node presents a cloud
// PAT (cloud_pat), and both are machine credentials. Only a human member —
// the workspace owner or any collaborator — may write a done-category status.
//
// The scope is the done LIFECYCLE CATEGORY, not the built-in `done` key alone.
// A custom status whose category is `done` is the same claim under another
// name, and the category is what the platform already uses to decide that an
// issue is complete (child_done wakeups, stage frontiers). `cancelled` is NOT
// covered: cancelling work is not a completion claim.
//
// The guard sits on the HTTP write path only. Server-side automation that
// finishes an issue without a request (delegated failure recovery, wakeup
// bookkeeping) never passes through here and keeps working.

// isMachineAuthoredRequest reports whether the request was authored by a
// machine rather than by a human member.
func isMachineAuthoredRequest(r *http.Request) bool {
	// Server-stamped actor source: an agent run (task token) or a cloud node
	// (cloud PAT). The auth middleware strips any client-supplied value before
	// stamping its own, so a recognized value here is authoritative.
	if isMachineCredentialActor(r) {
		return true
	}
	// A request that carries a task-scoped agent identity is an agent run
	// even when the actor source is absent. Both headers are required: a
	// lone agent id is not proof of an agent run.
	return r.Header.Get("X-Agent-ID") != "" && r.Header.Get("X-Task-ID") != ""
}

// issueCompletionRefused reports whether a machine-authored write must be
// refused for landing the issue in the given lifecycle category.
func issueCompletionRefused(machine bool, category string) bool {
	return machine && category == issuestatus.CategoryDone
}

// guardIssueCompletion refuses a machine-authored write that would put the
// issue into a done-category status. It returns false when the write was
// refused; the response has already been written then.
//
// An empty statusKey means the write does not touch status, and nothing is
// guarded. A human write is never refused here.
func (h *Handler) guardIssueCompletion(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, statusKey string) bool {
	if statusKey == "" {
		return true
	}
	if !isMachineAuthoredRequest(r) {
		return true
	}
	category, err := issuestatus.CategoryWithError(r.Context(), h.Queries, workspaceID, statusKey)
	if err != nil {
		// Fail closed: a write whose completion category cannot be resolved
		// is not a write this guard can clear.
		writeError(w, http.StatusInternalServerError, "failed to resolve issue status")
		return false
	}
	if !issueCompletionRefused(true, category) {
		return true
	}
	writeErrorCode(w, http.StatusForbidden, "agent_cannot_complete_issue",
		"only a human can mark an issue as done; move it to in_review and let a human close it")
	return false
}
