package powercontext

// Scope mapping (docs/powercontext-seam.md).
//
// PowerContext has ONE flat Scope per read. Multica has four identities —
// workspace, project, agent, issue — so exactly one of them names the scope
// and the others travel inside the text and the write path:
//
//   - project  -> the Scope id. Architecture, conventions and closed-issue
//     knowledge are project knowledge, and a project is what survives an
//     issue being closed.
//   - issue    -> the artifact written at run end (the handoff entry). It is
//     not a scope, so a task never needs its own Scope provisioned.
//   - agent    -> carried on the entry's kind/text; PowerContext's per-agent
//     narrowing is an Access-binding concern, not a scope string (see
//     Config.Token's note about the single administrator principal).
//   - workspace-> the fallback scope when a task has no project, because a
//     run with no scope at all would read nothing.
//
// The strings themselves stay Multica's D2 refs (server/internal/moct/refs.go)
// so both memory backends answer to the same vocabulary and a migration never
// rewrites stored refs.

const (
	// ProjectScopePrefix builds a project scope id: multica:project:<uuid>.
	ProjectScopePrefix = "multica:project:"
	// WorkspaceScopePrefix builds the fallback scope id: multica:ws:<uuid>.
	WorkspaceScopePrefix = "multica:ws:"
)

// ProjectScope is the scope id for a Multica project.
func ProjectScope(projectID string) string { return ProjectScopePrefix + projectID }

// WorkspaceScope is the scope id for a Multica workspace.
func WorkspaceScope(workspaceID string) string { return WorkspaceScopePrefix + workspaceID }

// ScopeForTask picks the scope a run reads. A project scope wins; a task with
// no project falls back to its workspace. ok is false when the task carries
// neither id, and the caller then runs without memory rather than inventing a
// scope the server would reject.
func ScopeForTask(projectID, workspaceID string) (scope string, ok bool) {
	if projectID != "" {
		return ProjectScope(projectID), true
	}
	if workspaceID != "" {
		return WorkspaceScope(workspaceID), true
	}
	return "", false
}
