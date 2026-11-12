package daemon

// ask/plan tool delivery (S1-A2, M-119): the second half of the card's "one
// mechanism for every runtime". The daemon is the only side that knows both
// facts the tools need — this host's multica-be URL and the path of the
// running `multica` binary — so it adds a `multica` stdio MCP server
// (`multica askplan-mcp`) to the task's merged MCP config, beside the moct
// entry (moct_memory.go), and every runtime that consumes the overlay
// (cursor / openclaw / opencode / codex / hermes / omp-dsh) picks it up.
//
// Env carries the run's identity — MULTICA_SERVER_URL, MULTICA_TOKEN (the
// mat_ task token), MULTICA_WORKSPACE_ID, MULTICA_TASK_ID — never tool
// arguments, so the tool schemas cannot even express a call outside this
// run's task; multica-be re-checks the token's task binding anyway
// (handler/askplan.go callerRunningIssueTask).
//
// Degradation, same rule as moct and the plugin hooks: never fatal. A task
// with no issue (S1-A1 refuses ask/plan on it), a missing token, an unusable
// URL or an unlocatable binary skips the entry and the task runs with the MCP
// servers it already had. The daemon additionally refuses the entry when the
// runtime itself cannot carry MCP servers (agent.SupportsTaskMCP — builtin pi
// below its MCP-extension floor would fail the LAUNCH on it), degrading to no
// tools rather than a dead task. Validate-before-inject matters here: an MCP
// server that fails its handshake takes an ACP/dsh session down
// (server/pkg/agent/acp_session.go).
//
// Boundary: this talks to multica-be only. Nothing on this path reaches MOCT.

import (
	"encoding/json"
	"strings"
)

// askPlanMCPServerName is the name the `askplan-mcp` server gets in a task's
// MCP config, and the name its initialize reports. A bare TOML key, which
// Codex's config writer requires (same constraint as moct).
const askPlanMCPServerName = "multica"

// askPlanMCPConfig builds the MCP overlay entry that gives a run the ask and
// plan tools, or ok=false when the entry must stay out of this run's config.
func askPlanMCPConfig(serverBaseURL string, task Task) (json.RawMessage, bool) {
	if task.WorkspaceID == "" || task.IssueID == "" || task.ID == "" {
		return nil, false
	}
	token := strings.TrimSpace(task.AuthToken)
	if !strings.HasPrefix(token, "mat_") {
		// Without the task-scoped token every call would 403; the run
		// itself is about to refuse to start on this same fact
		// (taskScopedAuthToken), so add nothing here.
		return nil, false
	}
	// Normalize is idempotent for http(s) and rejects a ws:// that somehow
	// reached us — the tool client needs an HTTP base.
	base, err := NormalizeServerBaseURL(serverBaseURL)
	if err != nil || base == "" {
		return nil, false
	}
	// Validate once here rather than letting a bad child spawn fail the
	// session: locate the same running binary the daemon's other helpers
	// use, and skip the entry when it cannot be resolved.
	executable, err := resolveSelfExecutable()
	if err != nil || strings.TrimSpace(executable) == "" {
		return nil, false
	}
	env := map[string]string{
		"MULTICA_SERVER_URL":   base,
		"MULTICA_TOKEN":        token,
		"MULTICA_WORKSPACE_ID": task.WorkspaceID,
		"MULTICA_TASK_ID":      task.ID,
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		askPlanMCPServerName: map[string]any{
			"command": strings.TrimSpace(executable),
			"args":    []string{"askplan-mcp"},
			"env":     env,
		},
	}})
	if err != nil {
		// Unreachable for this shape (a map of strings), kept so the
		// function has no error return to forget at the callsite.
		return nil, false
	}
	return raw, true
}
