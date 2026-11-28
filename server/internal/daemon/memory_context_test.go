package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/initctx"
)

// TestMemoryBackendFor pins the switch: nothing set is off, one URL picks its
// backend, and both set keeps the older seam while reporting the conflict. A
// host must never read two memory planes into one run.
func TestMemoryBackendFor(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		wantKind  memoryBackend
		wantBase  string
		wantToken string
		wantClash bool
	}{
		{
			name:     "off",
			cfg:      Config{},
			wantKind: memoryBackendNone,
		},
		{
			name:     "moct only",
			cfg:      Config{MoctBaseURL: "http://127.0.0.1:8110"},
			wantKind: memoryBackendMoct,
			wantBase: "http://127.0.0.1:8110",
		},
		{
			name:      "powercontext only",
			cfg:       Config{PowerContextBaseURL: " http://127.0.0.1:8000 ", PowerContextToken: " tok "},
			wantKind:  memoryBackendPowerContext,
			wantBase:  "http://127.0.0.1:8000",
			wantToken: "tok",
		},
		{
			name:      "both set keeps moct",
			cfg:       Config{MoctBaseURL: "http://127.0.0.1:8110", PowerContextBaseURL: "http://127.0.0.1:8000"},
			wantKind:  memoryBackendMoct,
			wantBase:  "http://127.0.0.1:8110",
			wantClash: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, base, token := memoryBackendFor(tc.cfg)
			if kind != tc.wantKind || base != tc.wantBase || token != tc.wantToken {
				t.Fatalf("memoryBackendFor = (%q,%q,%q), want (%q,%q,%q)",
					kind, base, token, tc.wantKind, tc.wantBase, tc.wantToken)
			}
			if got := memoryConflict(tc.cfg); got != tc.wantClash {
				t.Fatalf("memoryConflict = %v, want %v", got, tc.wantClash)
			}
		})
	}
}

// TestPowerContextInjectPromptOffPaths covers every "no memory, and here is
// why" outcome. Each one must still return a memory part: a run with no memory
// is legitimate, but a run row that cannot say which reason applied is not.
func TestPowerContextInjectPromptOffPaths(t *testing.T) {
	off := false
	cases := []struct {
		name     string
		cfg      Config
		task     Task
		wantNote string
	}{
		{
			name:     "seam off",
			cfg:      Config{},
			task:     moctTestTask(),
			wantNote: "no memory backend is configured",
		},
		{
			name:     "workspace switched it off",
			cfg:      Config{PowerContextBaseURL: "http://127.0.0.1:1"},
			task:     withMemorySwitch(moctTestTask(), &off),
			wantNote: "workspace switched the pre-run memory block off",
		},
		{
			name:     "no identity",
			cfg:      Config{PowerContextBaseURL: "http://127.0.0.1:1"},
			task:     Task{ID: "task-1"},
			wantNote: "no project or workspace identity",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, part := memoryInjectPrompt(context.Background(), tc.cfg, tc.task, moctTestLogger())
			if block != "" {
				t.Fatalf("block = %q, want empty", block)
			}
			if !part.Measured || part.Part != initctx.PartMemory {
				t.Fatalf("part = %+v, want a measured memory row", part)
			}
			if !strings.Contains(part.Note, tc.wantNote) {
				t.Fatalf("note = %q, want it to mention %q", part.Note, tc.wantNote)
			}
		})
	}
}

// daemonTestScopeID is the id the fake planes hand back for the task's Multica
// ref. PowerContext generates Scope ids, so every daemon route test has to
// serve the listing that maps the ref to one.
const daemonTestScopeID = "scp_daemon_test_1"

// serveDaemonScopeListing answers GET /v1/scopes for whatever ref the client
// asks about and reports whether it handled the request.
func serveDaemonScopeListing(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v1/scopes" {
		return false
	}
	ref := r.URL.Query().Get("query")
	_, _ = fmt.Fprintf(w, `{"items":[{"scope_id":%q,"external_references":[{"kind":"multica-project","value":%q}]}],"next_cursor":null}`,
		daemonTestScopeID, ref)
	return true
}

// TestPowerContextInjectPromptReady asserts the contract with the server: one
// scope derived from the task's project, a budget derived from the memory
// part's token budget, the content returned verbatim, and a run-row note that
// names the bytes and the scope.
func TestPowerContextInjectPromptReady(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDaemonScopeListing(w, r) {
			return
		}
		if r.URL.Path != "/v1/context/prepare" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"status":"ready","content":"### Memory\n\nKeep the seam one writer.","content_bytes":41}`))
	}))
	defer srv.Close()

	block, part := memoryInjectPrompt(context.Background(),
		Config{PowerContextBaseURL: srv.URL}, moctTestTask(), moctTestLogger())

	if gotBody["scope_id"] != daemonTestScopeID {
		t.Fatalf("scope_id = %v, want the resolved Scope id", gotBody["scope_id"])
	}
	if gotBody["max_bytes"] != float64(powerContextMaxBytes()) {
		t.Fatalf("max_bytes = %v, want %d", gotBody["max_bytes"], powerContextMaxBytes())
	}
	if !strings.Contains(block, "Keep the seam one writer.") {
		t.Fatalf("block = %q", block)
	}
	if !strings.HasPrefix(block, "## Project memory (PowerContext)") {
		t.Fatalf("block = %q, want the PowerContext heading", block)
	}
	if !strings.Contains(part.Note, "41 bytes") || !strings.Contains(part.Note, "multica:project:") {
		t.Fatalf("note = %q", part.Note)
	}
	if part.Tokens <= 0 {
		t.Fatalf("tokens = %d, want a priced block", part.Tokens)
	}
}

// TestPowerContextInjectPromptEmpty keeps "nothing to say" distinguishable
// from "backend broken": both inject nothing, but only one is a fault.
func TestPowerContextInjectPromptEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDaemonScopeListing(w, r) {
			return
		}
		_, _ = w.Write([]byte(`{"status":"empty","content":null,"content_bytes":0}`))
	}))
	defer srv.Close()

	block, part := memoryInjectPrompt(context.Background(),
		Config{PowerContextBaseURL: srv.URL}, moctTestTask(), moctTestLogger())
	if block != "" {
		t.Fatalf("block = %q, want empty", block)
	}
	if !strings.Contains(part.Note, "returned no context for scope") {
		t.Fatalf("note = %q", part.Note)
	}
}

// TestPowerContextInjectPromptHTTPError asserts a configuration fault (an
// unreadable scope) is named on the row, not collapsed into "unreachable".
func TestPowerContextInjectPromptHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDaemonScopeListing(w, r) {
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"scope_not_readable","message":"no scope"}}`))
	}))
	defer srv.Close()

	block, part := memoryInjectPrompt(context.Background(),
		Config{PowerContextBaseURL: srv.URL}, moctTestTask(), moctTestLogger())
	if block != "" {
		t.Fatalf("block = %q, want empty", block)
	}
	if !strings.Contains(part.Note, "403") || !strings.Contains(part.Note, "scope_not_readable") {
		t.Fatalf("note = %q, want the status and the server's code", part.Note)
	}
}

func TestPowerContextInjectPromptUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	block, part := memoryInjectPrompt(context.Background(),
		Config{PowerContextBaseURL: base}, moctTestTask(), moctTestLogger())
	if block != "" {
		t.Fatalf("block = %q, want empty", block)
	}
	if !strings.Contains(part.Note, "unreachable") {
		t.Fatalf("note = %q, want the outage named as such", part.Note)
	}
}

// TestMemoryInjectPromptPrefersMoctWhenBothSet guards the mutual exclusion at
// the call site, not just in the selector: with both URLs configured the
// PowerContext server must see no traffic at all.
func TestMemoryInjectPromptPrefersMoctWhenBothSet(t *testing.T) {
	pcCalls := 0
	pc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pcCalls++
		_, _ = w.Write([]byte(`{"status":"ready","content":"pc","content_bytes":2}`))
	}))
	defer pc.Close()
	moctSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"entries":[],"tokens":0}`))
	}))
	defer moctSrv.Close()

	block, part := memoryInjectPrompt(context.Background(),
		Config{MoctBaseURL: moctSrv.URL, PowerContextBaseURL: pc.URL}, moctTestTask(), moctTestLogger())
	if pcCalls != 0 {
		t.Fatalf("PowerContext was called %d times while moct-be owns the host", pcCalls)
	}
	if block != "" {
		t.Fatalf("block = %q, want moct's empty answer", block)
	}
	if !strings.Contains(part.Note, "moct") {
		t.Fatalf("note = %q, want it to attribute the answer to moct", part.Note)
	}
}

// TestPowerContextMCPConfigShape pins the overlay entry: PowerContext ships its
// own MCP endpoint, so the entry is the remote URL with the bearer header, not
// a stdio command.
func TestPowerContextMCPConfigShape(t *testing.T) {
	raw, ok := powerContextMCPConfig("http://127.0.0.1:8000/", "tok", moctTestTask())
	if !ok {
		t.Fatalf("expected an MCP entry")
	}
	var parsed struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Command string            `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entry, ok := parsed.MCPServers[powerContextMCPServerName]
	if !ok {
		t.Fatalf("entry missing from %s", raw)
	}
	if entry.Type != "http" || entry.URL != "http://127.0.0.1:8000/mcp" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("headers = %+v", entry.Headers)
	}
	if entry.Command != "" {
		t.Fatalf("entry carries a command (%q); PowerContext is a remote server", entry.Command)
	}
}

func TestPowerContextMCPConfigWithoutToken(t *testing.T) {
	raw, ok := powerContextMCPConfig("http://127.0.0.1:8000", "", moctTestTask())
	if !ok {
		t.Fatalf("expected an MCP entry")
	}
	if strings.Contains(string(raw), "Authorization") {
		t.Fatalf("entry sent an Authorization header with no token: %s", raw)
	}
}

func TestPowerContextMCPConfigSkips(t *testing.T) {
	if _, ok := powerContextMCPConfig("", "tok", moctTestTask()); ok {
		t.Fatalf("seam off must not add an entry")
	}
	if _, ok := powerContextMCPConfig("http://127.0.0.1:8000", "tok", Task{ID: "task-1"}); ok {
		t.Fatalf("a task with no scope must not get memory tools")
	}
	if _, ok := powerContextMCPConfig("not-a-url", "tok", moctTestTask()); ok {
		t.Fatalf("an unparseable URL must not add an entry")
	}
}

// TestPowerContextRecordHandoffWrites asserts step 13: the entry names the
// stable marker, the issue ref, the session and the outcome, so the next run's
// prepare recalls something a reader can act on.
func TestPowerContextRecordHandoffWrites(t *testing.T) {
	var remembers []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDaemonScopeListing(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/memory/search":
			_, _ = w.Write([]byte(`{"mode":"text","hits":[]}`))
		case "/v1/memory/remember":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			remembers = append(remembers, body)
			_, _ = w.Write([]byte(`{"changed":true,"records":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	task := moctTestTask()
	result := TaskResult{Status: "completed", SessionID: "sess-9", BranchName: "feat/x", Comment: "Moved the refresh into its own handler."}
	powerContextRecordHandoff(context.Background(), Config{PowerContextBaseURL: srv.URL}, task, result, moctTestLogger())

	if len(remembers) != 1 {
		t.Fatalf("remember calls = %d, want 1", len(remembers))
	}
	text, _ := remembers[0]["text"].(string)
	for _, want := range []string{
		powerContextHandoffMarker(task),
		"multica:issue:" + moctTestIssueID,
		"session: sess-9",
		"outcome: completed",
		"branch: feat/x",
		"Moved the refresh into its own handler.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("handoff text missing %q:\n%s", want, text)
		}
	}
	if remembers[0]["scope_id"] != daemonTestScopeID {
		t.Fatalf("scope_id = %v, want the resolved Scope id", remembers[0]["scope_id"])
	}
}

// TestPowerContextRecordHandoffIsIdempotent: the same run reported twice must
// write once. A second copy would inject twice on the next claim.
func TestPowerContextRecordHandoffIsIdempotent(t *testing.T) {
	remembers := 0
	task := moctTestTask()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveDaemonScopeListing(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/memory/search":
			// A 1.6.0 answer: the entry sits under `memory`, and its body is
			// `text`. Reading a top-level `content` here is how a dedupe check
			// silently stops finding anything.
			_, _ = w.Write([]byte(`{"mode":"text","hits":[{"memory":{"text":"` + powerContextHandoffMarker(task) + `\noutcome: completed"}}]}`))
		case "/v1/memory/remember":
			remembers++
			_, _ = w.Write([]byte(`{"changed":true,"records":[]}`))
		}
	}))
	defer srv.Close()

	cfg := Config{PowerContextBaseURL: srv.URL}
	powerContextRecordHandoff(context.Background(), cfg, task, TaskResult{Status: "completed", SessionID: "s"}, moctTestLogger())
	powerContextRecordHandoff(context.Background(), cfg, task, TaskResult{Status: "completed", SessionID: "s"}, moctTestLogger())
	if remembers != 0 {
		t.Fatalf("remember calls = %d, want 0 for an already-committed handoff", remembers)
	}
}

func TestPowerContextRecordHandoffSkips(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"mode":"text","hits":[]}`))
	}))
	defer srv.Close()

	// Wrong backend: the host runs moct-be, so PowerContext must stay silent.
	powerContextRecordHandoff(context.Background(),
		Config{MoctBaseURL: srv.URL}, moctTestTask(),
		TaskResult{SessionID: "s"}, moctTestLogger())
	// Nothing to remember: no session, no branch, no report.
	powerContextRecordHandoff(context.Background(),
		Config{PowerContextBaseURL: srv.URL}, moctTestTask(), TaskResult{}, moctTestLogger())
	if calls != 0 {
		t.Fatalf("backend calls = %d, want 0", calls)
	}
}

func TestPowerContextClampKeepsRunes(t *testing.T) {
	// A multi-byte rune straddling the cut must not be split.
	in := strings.Repeat("é", 100) // 200 bytes
	got := powerContextClamp(in, 51)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clamped text has no ellipsis: %q", got)
	}
	body := strings.TrimSuffix(got, "…")
	if len(body) > 51 {
		t.Fatalf("clamped body is %d bytes, want <= 51", len(body))
	}
	for _, r := range body {
		if string(r) != "é" {
			t.Fatalf("clamping split a rune: %q", body)
		}
	}
	if powerContextClamp("short", 10) != "short" {
		t.Fatalf("short text must pass through unchanged")
	}
}

func withMemorySwitch(task Task, on *bool) Task {
	task.MoctMemoryInject = on
	return task
}
