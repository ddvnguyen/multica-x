package moctmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/moct"
)

const (
	testWorkspaceRef = "multica:ws:11111111-1111-4111-8111-111111111111"
	testExpertRef    = "multica:agent:22222222-2222-4222-8222-222222222222"
	testTaskRef      = "multica:issue:33333333-3333-4333-8333-333333333333"
	testChannelRef   = "multica:project:44444444-4444-4444-8444-444444444444"
)

func testScope() Scope {
	return Scope{
		WorkspaceRef: testWorkspaceRef,
		ExpertRef:    testExpertRef,
		TaskRef:      testTaskRef,
		ChannelRef:   testChannelRef,
	}
}

func testClient(t *testing.T, handler http.HandlerFunc) *moct.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := moct.NewClient(moct.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

type frame struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// drive sends one JSON-RPC conversation and returns the parsed reply frames.
func drive(t *testing.T, client *moct.Client, scope Scope, lines ...string) []frame {
	t.Helper()
	var in bytes.Buffer
	for _, line := range lines {
		in.WriteString(line)
		in.WriteString("\n")
	}
	var out bytes.Buffer
	server := New(client, scope, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := server.Run(context.Background(), &in, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var frames []frame
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var f frame
		if err := decoder.Decode(&f); err != nil {
			t.Fatalf("decode reply %q: %v", out.String(), err)
		}
		frames = append(frames, f)
	}
	return frames
}

func toolText(t *testing.T, f frame) string {
	t.Helper()
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(f.Result, &result); err != nil {
		t.Fatalf("decode tool result %s: %v", f.Result, err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool result has no content: %s", f.Result)
	}
	return result.Content[0].Text
}

func TestInitializeAndToolsList(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	// The notification takes no reply, so exactly two frames come back.
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(frames), frames)
	}
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		ServerInfo      map[string]any `json:"serverInfo"`
	}
	if err := json.Unmarshal(frames[0].Result, &init); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if init.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %q", init.ProtocolVersion)
	}
	if init.ServerInfo["name"] != "moct" {
		t.Errorf("serverInfo.name = %v", init.ServerInfo["name"])
	}

	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema any    `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(frames[1].Result, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("tool %s has no inputSchema", tool.Name)
		}
	}
	for _, want := range []string{"memory_search", "memory_get", "memory_note"} {
		if !names[want] {
			t.Errorf("tools/list missing %s (got %v)", want, names)
		}
	}
}

func TestUnknownMethodIsProtocolError(t *testing.T) {
	frames := drive(t, testClient(t, func(http.ResponseWriter, *http.Request) {}), testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != -32601 {
		t.Fatalf("want -32601, got %+v", frames)
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	frames := drive(t, testClient(t, func(http.ResponseWriter, *http.Request) {}), testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_drop","arguments":{}}}`)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != -32602 {
		t.Fatalf("want -32602, got %+v", frames)
	}
}

// Scope must come from env, not from tool arguments: assert the query moct-be
// actually receives carries the refs of this run and nothing the caller sent.
func TestMemorySearchSendsEnvScope(t *testing.T) {
	var gotQuery url.Values
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/memory" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{"results":[{"id":"rec-1","type":"convention","title":"T","body":"B"}],"total":1}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"q":"deploy lane","types":["convention"],"limit":5}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if gotQuery.Get("workspaceId") != testWorkspaceRef || gotQuery.Get("expertId") != testExpertRef {
		t.Errorf("scope not taken from env: %v", gotQuery)
	}
	if gotQuery.Get("tasks") != testTaskRef {
		t.Errorf("tasks = %q", gotQuery.Get("tasks"))
	}
	if gotQuery.Get("channels") != testChannelRef {
		t.Errorf("channels = %q", gotQuery.Get("channels"))
	}
	if gotQuery.Get("q") != "deploy lane" || gotQuery.Get("limit") != "5" || gotQuery.Get("types") != "convention" {
		t.Errorf("tool arguments not forwarded: %v", gotQuery)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "rec-1") {
		t.Errorf("result should carry the records: %s", body)
	}
}

func TestMemoryGetForwardsIDs(t *testing.T) {
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/memory/get" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"records":[],"missing":["rec-2"]}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_get","arguments":{"ids":["rec-1","rec-2"]}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if !strings.Contains(string(gotBody), `"rec-1"`) || !strings.Contains(string(gotBody), `"rec-2"`) {
		t.Errorf("ids not forwarded: %s", gotBody)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "rec-2") {
		t.Errorf("missing ids should be reported: %s", body)
	}
}

// More than 10 ids is a client-side contract violation; it must come back as a
// tool error the agent can correct, not a protocol error and not a crash.
func TestMemoryGetRejectsBadIDBatch(t *testing.T) {
	calls := 0
	client := testClient(t, func(http.ResponseWriter, *http.Request) { calls++ })
	ids := `["a1","a2","a3","a4","a5","a6","a7","a8","a9","a10","a11"]`
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_get","arguments":{"ids":`+ids+`}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "1..10") {
		t.Errorf("expected the id-count contract error, got %s", body)
	}
	if calls != 0 {
		t.Errorf("moct-be should never see an illegal request; got %d calls", calls)
	}
}

func TestMemoryNoteDefaultsToTaskHomeAndEnvIdentity(t *testing.T) {
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/memory" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"record":{"id":"rec-new"}}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"decision","title":"Use lane z","body":"Everything loopback.","attrs":{"decidedBy":"owner"}}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	var sent moct.NoteParams
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent note: %v (%s)", err, gotBody)
	}
	if sent.Home.Kind != "task" || sent.Home.Ref != testTaskRef {
		t.Errorf("home = %+v, want the run's task home", sent.Home)
	}
	if sent.WorkspaceID != testWorkspaceRef {
		t.Errorf("workspaceId = %q, must come from env", sent.WorkspaceID)
	}
	if sent.Actor.Kind != "member" || sent.Actor.ID != testExpertRef {
		t.Errorf("actor = %+v, must come from env", sent.Actor)
	}
	if sent.Type != "decision" || sent.Title != "Use lane z" || sent.Body != "Everything loopback." {
		t.Errorf("tool arguments not forwarded: %+v", sent)
	}
	if sent.Attrs["decidedBy"] != "owner" {
		t.Errorf("attrs not forwarded: %+v", sent.Attrs)
	}
}

func TestMemoryNoteForwardsExplicitHomeAndConfirmNew(t *testing.T) {
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"record":{"id":"rec-new"}}`)
	})
	drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B","home":{"kind":"channel","ref":"`+testChannelRef+`"},"confirmNew":true}}}`)
	var sent moct.NoteParams
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent note: %v (%s)", err, gotBody)
	}
	if sent.Home.Kind != "channel" || sent.Home.Ref != testChannelRef {
		t.Errorf("explicit home not honoured: %+v", sent.Home)
	}
	if !sent.ConfirmNew {
		t.Error("confirmNew not forwarded")
	}
}

// A run that is not attached to an issue defaults to the team home, so a
// convention/decision stays writable from any run (M-605).
func TestMemoryNoteWithoutTaskRefDefaultsToTeamHome(t *testing.T) {
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"record":{"id":"rec-team"}}`)
	})
	scope := testScope()
	scope.TaskRef = ""
	frames := drive(t, client, scope,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B"}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	var sent moct.NoteParams
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent note: %v (%s)", err, gotBody)
	}
	if sent.Home.Kind != "team" || sent.Home.Ref != testWorkspaceRef {
		t.Errorf("home = %+v, want the run's team home", sent.Home)
	}
}

// The seam's failure mode, on the tool path: moct-be down is a tool error and
// the server keeps serving. An exit here would take the session down with it.
func TestMoctBeDownIsToolErrorAndServerStaysUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client, err := moct.NewClient(moct.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close() // now unreachable

	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{"q":"x"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(frames) != 2 {
		t.Fatalf("server should keep answering after a failed call: %+v", frames)
	}
	if frames[0].Error != nil {
		t.Fatalf("a backend outage must be a tool error, not a protocol error: %+v", frames[0])
	}
	if !strings.Contains(toolText(t, frames[0]), "moct-be unavailable") {
		t.Errorf("tool error should name the outage: %s", toolText(t, frames[0]))
	}
	if frames[1].Error != nil {
		t.Errorf("ping after the failure: %+v", frames[1])
	}
}

// A server started with no client must still answer every call, as a tool
// error — misconfiguration must not become an exit that kills the session.
func TestUnconfiguredServerAnswersToolErrors(t *testing.T) {
	frames := drive(t, nil, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(frames) != 2 {
		t.Fatalf("got %+v", frames)
	}
	if frames[0].Error != nil {
		t.Fatalf("want a tool error, got %+v", frames[0])
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "not configured") {
		t.Errorf("tool error should say the seam is off: %s", body)
	}
	// The tool list still works: the agent can discover the tools and read why
	// they are failing instead of seeing a dead server.
	if frames[1].Error != nil {
		t.Errorf("tools/list must keep working: %+v", frames[1])
	}
}

func TestIncompleteScopeIsToolError(t *testing.T) {
	frames := drive(t, testClient(t, func(http.ResponseWriter, *http.Request) {}), Scope{WorkspaceRef: testWorkspaceRef},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_get","arguments":{"ids":["rec-1"]}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "not configured") {
		t.Errorf("tool error should say the scope is incomplete: %s", body)
	}
}

func TestScopeCompleteness(t *testing.T) {
	if !testScope().Complete() {
		t.Error("full scope should be complete")
	}
	if (Scope{WorkspaceRef: testWorkspaceRef}).Complete() {
		t.Error("workspace-only scope is not complete")
	}
	if (Scope{ExpertRef: testExpertRef}).Complete() {
		t.Error("expert-only scope is not complete")
	}
}

// M-605/1: {kind:"team"} without a ref fills in this run's workspace instead
// of failing or forwarding a ref moct-be would 403.
func TestMemoryNoteTeamKindWithoutRefFillsWorkspace(t *testing.T) {
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"record":{"id":"rec-team"}}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B","home":{"kind":"team"}}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	var sent moct.NoteParams
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode sent note: %v (%s)", err, gotBody)
	}
	if sent.Home.Kind != "team" || sent.Home.Ref != testWorkspaceRef {
		t.Errorf("home = %+v, want team home of this run's workspace", sent.Home)
	}
}

// M-605/2: supersedes travels top-level (moct-be reads body.supersedes), and
// the legacy attrs.supersedes shape is promoted rather than 400'd.
func TestMemoryNoteSupersedesTopLevelAndAttrsPromoted(t *testing.T) {
	var bodies [][]byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"record":{"id":"rec-new","supersedes":"mem_old"}}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"New","body":"B","home":{"kind":"team","ref":"`+testWorkspaceRef+`"},"supersedes":"mem_old"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"New2","body":"B","home":{"kind":"team","ref":"`+testWorkspaceRef+`"},"attrs":{"supersedes":"mem_older"}}}}`)
	if len(frames) != 2 || frames[0].Error != nil || frames[1].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	var first moct.NoteParams
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("decode first note: %v", err)
	}
	if first.Supersedes == nil || *first.Supersedes != "mem_old" {
		t.Errorf("first supersedes = %+v, want mem_old top-level", first.Supersedes)
	}
	var second map[string]json.RawMessage
	if err := json.Unmarshal(bodies[1], &second); err != nil {
		t.Fatalf("decode second note: %v", err)
	}
	var top *string
	if raw, ok := second["supersedes"]; ok {
		_ = json.Unmarshal(raw, &top)
	}
	if top == nil || *top != "mem_older" {
		t.Errorf("second supersedes top-level = %s, want mem_older", second["supersedes"])
	}
	var attrs map[string]json.RawMessage
	if raw, ok := second["attrs"]; ok && string(raw) != "null" {
		_ = json.Unmarshal(raw, &attrs)
		if _, kept := attrs["supersedes"]; kept {
			t.Errorf("attrs.supersedes must be dropped (moct-be would 400 it): %s", raw)
		}
	}
}

// M-605/2: retract runs instead of a note, against moct-be's retract verb.
func TestMemoryNoteRetractWithdrawsRecord(t *testing.T) {
	var gotPath string
	var gotBody []byte
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"id":"mem_old","status":"retracted","version":2}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"unused","body":"unused","retract":"mem_old","retractVersion":1,"retractReason":"stale"}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if gotPath != "/team/v1/memory/mem_old/retract" {
		t.Errorf("path = %s, want the retract verb", gotPath)
	}
	var body struct {
		BaseVersion int        `json:"baseVersion"`
		Actor       moct.Actor `json:"actor"`
		Reason      string     `json:"reason"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("decode retract body: %v (%s)", err, gotBody)
	}
	if body.BaseVersion != 1 || body.Actor.ID != testExpertRef || body.Reason != "stale" {
		t.Errorf("retract body = %+v, want version 1, run actor, reason stale", body)
	}
}

// M-605/3: a moct-be SCOPE_FORBIDDEN names the accepted ref format so the
// next attempt does not burn retries on bare refs.
func TestMemoryNoteScopeRefusalNamesRefFormat(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"code":"SCOPE_FORBIDDEN","error":"team home must be the workspace"}`)
	})
	frames := drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_note","arguments":{"type":"convention","title":"T","body":"B","home":{"kind":"team","ref":"multica:ws:other"}}}}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("unexpected frames %+v", frames)
	}
	if body := toolText(t, frames[0]); !strings.Contains(body, "multica:ws:<workspace uuid>") {
		t.Errorf("scope refusal should name the accepted ref format, got %s", body)
	}
}
