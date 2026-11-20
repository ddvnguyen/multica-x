package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/moctapi"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProxyMoct forwards the closed MOCT read/approval allow-list after resolving
// the caller's workspace membership. Scope and actor values are never accepted
// from the browser.
func (h *Handler) ProxyMoct(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	identity := moctapi.Identity{WorkspaceID: workspaceID, ActorID: uuidToString(member.UserID), Admin: isWorkspaceAdmin(member)}
	identity.AgentInWorkspace = func(ctx context.Context, agentID string) (bool, error) {
		_, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(agentID), WorkspaceID: parseUUID(workspaceID)})
		if err != nil && err != pgx.ErrNoRows {
			return false, err
		}
		return err == nil, nil
	}
	rest := r.URL.Path
	prefix := "/api/workspaces/" + workspaceID + "/moct"
	if len(rest) >= len(prefix) {
		rest = rest[len(prefix):]
	}
	moctapi.New(os.Getenv("MOCT_BASE_URL")).Serve(w, r, identity, rest)
}

// GetTaskMemoryReport returns the persisted memory part of a run's initial
// context report. The enclosing issue lookup applies the same privacy checks
// as the issue's run history before the task row is read.
func (h *Handler) GetTaskMemoryReport(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: parseUUID(chi.URLParam(r, "taskId")), WorkspaceID: issue.WorkspaceID})
	if err != nil || task.IssueID != issue.ID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	memory := memoryReportPart(task.Result)
	writeJSON(w, http.StatusOK, struct {
		TaskID      string          `json:"task_id"`
		WorkspaceID string          `json:"workspace_id"`
		Memory      json.RawMessage `json:"memory"`
	}{TaskID: uuidToString(task.ID), WorkspaceID: workspaceID, Memory: memory})
}

func memoryReportPart(result json.RawMessage) json.RawMessage {
	var completed map[string]json.RawMessage
	if len(result) == 0 || json.Unmarshal(result, &completed) != nil {
		return json.RawMessage("null")
	}
	var report struct {
		Parts []json.RawMessage `json:"parts"`
	}
	if raw := completed["init_context"]; len(raw) == 0 || json.Unmarshal(raw, &report) != nil {
		return json.RawMessage("null")
	}
	for _, part := range report.Parts {
		var header struct {
			Part string `json:"part"`
		}
		if json.Unmarshal(part, &header) == nil && header.Part == "memory" {
			return part
		}
	}
	return json.RawMessage("null")
}
