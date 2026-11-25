package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/moctapi"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProxyMoct forwards the closed MOCT read/approval allow-list after resolving
// the caller's workspace membership. Scope and actor values are never accepted
// from the browser. Create and purge are human-only on top of owner/admin:
// agent task tokens bind X-User-ID to the runtime owner's user, so a member
// check alone would let an agent file human-authored notes that skip the
// approval gate — the machine credential is refused even when the agent's
// owner is an admin (M-570). moct-be itself reserves purge for the human,
// and delete must stay deliberate.
func (h *Handler) ProxyMoct(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	if isMachineCredentialActor(r) && (isMoctPurgeRequest(r) || isMoctCreateRequest(r)) {
		writeError(w, http.StatusForbidden, "memory writes need a human member with owner or admin role")
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
	// M-542: Role-knowledge containment for null-workspace expert-home
	// records. A Role in this workspace owns its knowledge here; anything
	// else keeps the legacy allowance (see moctapi lookup).
	identity.RoleInWorkspace = func(ctx context.Context, agentID string) (bool, error) {
		row, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(agentID), WorkspaceID: parseUUID(workspaceID)})
		if err != nil {
			if err == pgx.ErrNoRows {
				return false, nil
			}
			return false, err
		}
		return row.Subtype == "role", nil
	}
	rest := r.URL.Path
	prefix := "/api/workspaces/" + workspaceID + "/moct"
	if len(rest) >= len(prefix) {
		rest = rest[len(prefix):]
	}
	moctapi.New(os.Getenv("MOCT_BASE_URL")).Serve(w, r, identity, rest)
}

// isMoctPurgeRequest reports whether r targets the memory purge route. The
// match mirrors moctapi.match so the human-only gate cannot drift from the
// route it guards.
func isMoctPurgeRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	segments := splitMoctPath(r.URL.Path)
	for i := 0; i+4 < len(segments); i++ {
		if segments[i] == "team" && segments[i+1] == "v1" && segments[i+2] == "memory" && segments[i+4] == "purge" {
			id := segments[i+3]
			if len(id) >= 1 && len(id) <= 64 {
				ok := true
				for _, c := range id {
					if !(c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
						ok = false
						break
					}
				}
				if ok {
					return true
				}
			}
		}
	}
	return false
}

// isMoctCreateRequest reports whether r targets the memory create route. The
// match mirrors moctapi.match so the human-only gate cannot drift from the
// route it guards.
func isMoctCreateRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	segments := splitMoctPath(r.URL.Path)
	for i := 0; i+2 < len(segments); i++ {
		if segments[i] == "team" && segments[i+1] == "v1" && segments[i+2] == "memory" && i+3 == len(segments) {
			return true
		}
	}
	return false
}

func splitMoctPath(p string) []string {
	raw := strings.Split(strings.Trim(p, "/"), "/")
	out := raw[:0]
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
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
	// M-132 E3: the on-demand memory tools this run called, from its own stored
	// transcript. A failed read is reported as unavailable, never as "no calls".
	tools := memoryToolsReport{Available: false, Calls: []memoryToolCall{}}
	if rows, err := h.Queries.ListTaskMessages(r.Context(), task.ID); err == nil {
		tools = memoryToolsReport{Available: true, Calls: memoryToolCalls(rows)}
	}
	writeJSON(w, http.StatusOK, struct {
		TaskID      string            `json:"task_id"`
		WorkspaceID string            `json:"workspace_id"`
		Memory      json.RawMessage   `json:"memory"`
		MemoryTools memoryToolsReport `json:"memory_tools"`
	}{TaskID: uuidToString(task.ID), WorkspaceID: workspaceID, Memory: memory, MemoryTools: tools})
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
