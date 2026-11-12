package daemon

import (
	"encoding/json"
	"errors"
	"testing"
)

// S1-A2 (M-119): the daemon-side builder that mounts `multica askplan-mcp`
// into a task's MCP config. Mirrors the moct_memory_test.go shape so the two
// overlay entries are pinned the same way.

func askPlanTestTask() Task {
	return Task{
		ID:          "task-1",
		WorkspaceID: moctTestWorkspaceID,
		IssueID:     moctTestIssueID,
		AgentID:     moctTestAgentID,
		AuthToken:   "mat_task-token",
	}
}

type askPlanEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func askPlanServer(t *testing.T, raw json.RawMessage) askPlanEntry {
	t.Helper()
	var parsed struct {
		MCPServers map[string]askPlanEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	entry, ok := parsed.MCPServers[askPlanMCPServerName]
	if !ok {
		t.Fatalf("no %q server in %s", askPlanMCPServerName, raw)
	}
	return entry
}

// TestAskPlanMCPConfigShape pins the entry's command, subcommand and env —
// identity travels as ENV, never as tool arguments, so the tool schemas
// cannot express a call outside this run's task.
func TestAskPlanMCPConfigShape(t *testing.T) {
	originalResolve := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = originalResolve })
	resolveSelfExecutable = func() (string, error) { return "/usr/local/bin/multica", nil }

	raw, ok := askPlanMCPConfig("http://127.0.0.1:8080", askPlanTestTask())
	if !ok {
		t.Fatal("expected an MCP config")
	}
	entry := askPlanServer(t, raw)
	if entry.Command != "/usr/local/bin/multica" {
		t.Errorf("command = %q", entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "askplan-mcp" {
		t.Errorf("args = %v", entry.Args)
	}
	wantEnv := map[string]string{
		"MULTICA_SERVER_URL":   "http://127.0.0.1:8080",
		"MULTICA_TOKEN":        "mat_task-token",
		"MULTICA_WORKSPACE_ID": moctTestWorkspaceID,
		"MULTICA_TASK_ID":      "task-1",
	}
	if len(entry.Env) != len(wantEnv) {
		t.Errorf("env = %v, want exactly %d keys %v", entry.Env, len(wantEnv), wantEnv)
	}
	for key, want := range wantEnv {
		if entry.Env[key] != want {
			t.Errorf("env %s = %q, want %q", key, entry.Env[key], want)
		}
	}
}

// The ws:// default MULTICA_SERVER_URL hands the daemon must not reach the
// child as-is: the tool client speaks HTTP only. Normalize is idempotent for
// http(s) and converts ws/wss on the way in.
func TestAskPlanMCPConfigNormalizesServerURL(t *testing.T) {
	originalResolve := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = originalResolve })
	resolveSelfExecutable = func() (string, error) { return "/usr/local/bin/multica", nil }

	raw, ok := askPlanMCPConfig("ws://127.0.0.1:8080/ws", askPlanTestTask())
	if !ok {
		t.Fatal("expected an MCP config for a ws:// daemon URL")
	}
	if got := askPlanServer(t, raw).Env["MULTICA_SERVER_URL"]; got != "http://127.0.0.1:8080" {
		t.Errorf("MULTICA_SERVER_URL = %q", got)
	}
}

// Every skip condition keeps a broken or unusable entry out of the session —
// the entry must never be added when its own client would reject it.
func TestAskPlanMCPConfigSkipsWhenUnusable(t *testing.T) {
	originalResolve := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = originalResolve })

	cases := map[string]struct {
		baseURL string
		task    func(Task) Task
		resolve func() (string, error)
	}{
		"empty URL":    {baseURL: "", task: func(t Task) Task { return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"bad URL":      {baseURL: "not-a-url", task: func(t Task) Task { return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"no workspace": {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { t.WorkspaceID = ""; return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"no issue":     {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { t.IssueID = ""; return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"no task id":   {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { t.ID = ""; return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"no token":     {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { t.AuthToken = ""; return t }, resolve: func() (string, error) { return "/bin/multica", nil }},
		"non-task token": {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task {
			t.AuthToken = "mul_pat_not_task_scoped"
			return t
		}, resolve: func() (string, error) { return "/bin/multica", nil }},
		"unresolved exe": {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { return t }, resolve: func() (string, error) { return "", errors.New("nope") }},
		"empty exe":      {baseURL: "http://127.0.0.1:8080", task: func(t Task) Task { return t }, resolve: func() (string, error) { return "  ", nil }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resolveSelfExecutable = tc.resolve
			if raw, ok := askPlanMCPConfig(tc.baseURL, tc.task(askPlanTestTask())); ok {
				t.Errorf("expected no MCP config, got %s", raw)
			}
		})
	}
}

// The entry must ride the same by-name, overlay-wins merge as every other
// per-task overlay, keep existing servers untouched, and coexist with the
// moct entry in one merged config (the acceptance criterion: both present).
func TestAskPlanMCPConfigMergesAlongsideMoct(t *testing.T) {
	originalResolve := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = originalResolve })
	resolveSelfExecutable = func() (string, error) { return "/usr/local/bin/multica", nil }

	base := json.RawMessage(`{"mcpServers":{"composio":{"url":"http://127.0.0.1:9/"}}}`)

	askPlanConfig, ok := askPlanMCPConfig("http://127.0.0.1:8080", askPlanTestTask())
	if !ok {
		t.Fatal("expected an MCP config")
	}
	merged, err := mergeTaskRemoteMCPConfig(base, askPlanConfig)
	if err != nil {
		t.Fatalf("merge ask/plan: %v", err)
	}
	moctConfig, ok := moctMCPConfig("http://127.0.0.1:8310", moctTestTask())
	if !ok {
		t.Fatal("expected the moct MCP config")
	}
	merged, err = mergeTaskRemoteMCPConfig(merged, moctConfig)
	if err != nil {
		t.Fatalf("merge moct: %v", err)
	}

	var parsed struct {
		MCPServers map[string]askPlanEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	if _, ok := parsed.MCPServers[askPlanMCPServerName]; !ok {
		t.Errorf("ask/plan entry missing from merged config: %v", parsed.MCPServers)
	}
	if _, ok := parsed.MCPServers["moct"]; !ok {
		t.Errorf("moct entry missing from merged config: %v", parsed.MCPServers)
	}
	if entry, ok := parsed.MCPServers["composio"]; !ok || entry.Command != "" {
		t.Errorf("existing server was disturbed: %+v", parsed.MCPServers["composio"])
	}
	if got := parsed.MCPServers[askPlanMCPServerName].Args; len(got) != 1 || got[0] != "askplan-mcp" {
		t.Errorf("args = %v", got)
	}
	// The two entries share one blob by design. The boundary that matters is
	// checked in the askplanmcp package: it imports neither MOCT package.
}
