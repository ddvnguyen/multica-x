package askplanmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	testWorkspaceID = "11111111-1111-4111-8111-111111111111"
	testTaskID      = "22222222-2222-4222-8222-222222222222"
	testPlanID      = "33333333-3333-4333-8333-333333333333"
	testToken       = "mat_test-token"
)

func testScope() Scope {
	return Scope{WorkspaceID: testWorkspaceID, TaskID: testTaskID}
}

// testClient builds a client against a stub multica-be. The handler records
// the request it received so assertions can check method, path, auth and body.
type recorded struct {
	method string
	path   string
	auth   string
	body   []byte
}

func testClient(t *testing.T, respond func(w http.ResponseWriter, r *http.Request), rec *recorded) *APIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rec != nil {
			rec.method = r.Method
			rec.path = r.URL.Path
			rec.auth = r.Header.Get("Authorization")
			rec.body, _ = io.ReadAll(r.Body)
		}
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	client, err := NewAPIClient(srv.URL, testToken)
	if err != nil {
		t.Fatalf("NewAPIClient: %v", err)
	}
	return client
}

func okHandler(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
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
func drive(t *testing.T, client *APIClient, scope Scope, lines ...string) []frame {
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

// callOne runs a single tools/call with the test scope and returns its frame.
// The arguments must be ONE line: Run reads newline-delimited frames, and a
// literal newline in the source would split the call across frames.
func callOne(t *testing.T, client *APIClient, tool, args string) frame {
	t.Helper()
	return callOneIn(t, client, testScope(), tool, args)
}

// callOneIn is callOne with an explicit scope, for scope-gating tests.
func callOneIn(t *testing.T, client *APIClient, scope Scope, tool, args string) frame {
	t.Helper()
	frames := drive(t, client, scope,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args))
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1: %+v", len(frames), frames)
	}
	return frames[0]
}

// toolText decodes a successful tool result's text; it fails the test when
// the call came back as a protocol error instead of a result.
func toolText(t *testing.T, f frame) string {
	t.Helper()
	if f.Error != nil {
		t.Fatalf("expected a tool result, got protocol error %+v", f.Error)
	}
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
	if result.IsError {
		t.Fatalf("expected success, got tool error: %s", result.Content[0].Text)
	}
	return result.Content[0].Text
}

// toolErrorText asserts the call FAILED as a tool error and returns why.
func toolErrorText(t *testing.T, f frame) string {
	t.Helper()
	if f.Error != nil {
		t.Fatalf("expected a tool error result, got protocol error %+v", f.Error)
	}
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
	if !result.IsError || len(result.Content) == 0 {
		t.Fatalf("expected isError result, got: %s", f.Result)
	}
	return result.Content[0].Text
}

func TestInitializeAndToolsList(t *testing.T) {
	client := testClient(t, okHandler(`{}`), nil)
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
	// The name must match the overlay entry the daemon mounts (askplan_tools.go).
	if init.ServerInfo["name"] != ServerName || ServerName != "multica" {
		t.Errorf("serverInfo.name = %v", init.ServerInfo["name"])
	}

	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(frames[1].Result, &list); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(list.Tools) != 2 {
		t.Fatalf("got %d tools, want exactly ask and plan: %+v", len(list.Tools), list.Tools)
	}
	byName := map[string]map[string]any{}
	for _, tool := range list.Tools {
		byName[tool.Name] = tool.InputSchema
	}
	if byName["ask"] == nil || byName["plan"] == nil {
		t.Fatalf("tools/list must carry ask and plan, got %v", byName)
	}
}

// TestSchemaBoundsMatchLimits pins the JSON schema's numeric bounds to the
// package's limit constants, closing the chain the handler-side parity test
// starts: handler consts == these consts == what tools/list advertises.
func TestSchemaBoundsMatchLimits(t *testing.T) {
	tools := toolDescriptors()
	byName := map[string]map[string]any{}
	for _, tool := range tools {
		byName[tool["name"].(string)], _ = tool["inputSchema"].(map[string]any)
	}

	ask := byName["ask"]
	props := ask["properties"].(map[string]any)
	if got := props["context"].(map[string]any)["maxLength"]; got != AskContextLimit {
		t.Errorf("schema context.maxLength = %v, want %d", got, AskContextLimit)
	}
	questions := props["questions"].(map[string]any)
	if got := questions["maxItems"]; got != AskMaxQuestions {
		t.Errorf("schema questions.maxItems = %v, want %d", got, AskMaxQuestions)
	}
	qItem := questions["items"].(map[string]any)
	qProps := qItem["properties"].(map[string]any)
	if got := qProps["prompt"].(map[string]any)["maxLength"]; got != AskPromptLimit {
		t.Errorf("schema prompt.maxLength = %v, want %d", got, AskPromptLimit)
	}
	options := qProps["options"].(map[string]any)
	if got := options["minItems"]; got != AskMinOptions {
		t.Errorf("schema options.minItems = %v, want %d", got, AskMinOptions)
	}
	if got := options["maxItems"]; got != AskMaxOptions {
		t.Errorf("schema options.maxItems = %v, want %d", got, AskMaxOptions)
	}
	oProps := options["items"].(map[string]any)["properties"].(map[string]any)
	if got := oProps["label"].(map[string]any)["maxLength"]; got != AskLabelLimit {
		t.Errorf("schema label.maxLength = %v, want %d", got, AskLabelLimit)
	}
	if got := oProps["description"].(map[string]any)["maxLength"]; got != AskDescriptionLimit {
		t.Errorf("schema option description.maxLength = %v, want %d", got, AskDescriptionLimit)
	}

	plan := byName["plan"]
	pProps := plan["properties"].(map[string]any)
	if got := pProps["title"].(map[string]any)["maxLength"]; got != PlanTitleLimit {
		t.Errorf("schema title.maxLength = %v, want %d", got, PlanTitleLimit)
	}
	if got := pProps["outline"].(map[string]any)["maxItems"]; got != PlanMaxSections {
		t.Errorf("schema outline.maxItems = %v, want %d", got, PlanMaxSections)
	}
	if got := pProps["markdown"].(map[string]any)["maxLength"]; got != PlanSectionCharMax {
		t.Errorf("schema markdown.maxLength = %v, want %d", got, PlanSectionCharMax)
	}
	action := pProps["action"].(map[string]any)["enum"].([]string)
	if len(action) != 3 || action[0] != "create" || action[1] != "write" || action[2] != "submit" {
		t.Errorf("schema action.enum = %v", action)
	}
}

// TestAskParksViaPost is the card's core mapping: ask → POST the S1-A1 route
// with the task token, and the success text tells the agent the task parked.
// Extra identity fields smuggled into the arguments are dropped on the floor
// — the path comes from env scope only, so a call cannot leave this run.
func TestAskParksViaPost(t *testing.T) {
	var rec recorded
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"card_id":"card-1","issue_id":"iss-1","status":"open"}`)
	}, &rec)
	f := callOne(t, client, "ask", `{"context":"Which lane?",`+
		`"questions":[{"prompt":"Ship on which lane?","options":[{"label":"Lane A","description":"Day lane"},{"label":"Lane Z","description":"Night lane"}]}],`+
		`"workspace_id":"attacker-ws","task_id":"attacker-task"}`)
	text := toolText(t, f)
	if rec.method != "POST" {
		t.Errorf("method = %s", rec.method)
	}
	wantPath := "/api/workspaces/" + testWorkspaceID + "/tasks/" + testTaskID + "/ask"
	if rec.path != wantPath {
		t.Errorf("path = %s, want %s", rec.path, wantPath)
	}
	if rec.auth != "Bearer "+testToken {
		t.Errorf("auth = %q", rec.auth)
	}
	var sent askArgs
	if err := json.Unmarshal(rec.body, &sent); err != nil {
		t.Fatalf("sent body: %v", err)
	}
	if len(sent.Questions) != 1 || sent.Questions[0].Prompt != "Ship on which lane?" {
		t.Errorf("questions not forwarded: %s", rec.body)
	}
	if !strings.Contains(text, "card-1") || !strings.Contains(text, "blocked awaiting a human answer") {
		t.Errorf("success text should carry the card id and the park notice: %s", text)
	}
}

// TestAskValidationIsToolErrorWithoutHTTP mirrors the server's edge checks
// one-for-one; every rejection must happen locally with the server's wording.
func TestAskValidationIsToolErrorWithoutHTTP(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"no questions", `{}`, "questions must be 1 to 6"},
		{"seven questions", fmt.Sprintf(`{"questions":[%s]}`, strings.Repeat(
			`{"prompt":"Q","options":[{"label":"a"},{"label":"b"}]},`, 6)+`{"prompt":"Q","options":[{"label":"a"},{"label":"b"}]}`),
			"questions must be 1 to 6"},
		{"one option", `{"questions":[{"prompt":"Q","options":[{"label":"a"}]}]}`, "must have 2 to 4 options"},
		{"empty prompt", `{"questions":[{"prompt":"","options":[{"label":"a"},{"label":"b"}]}]}`, "prompt must be 1 to 300 characters"},
		{"long label", fmt.Sprintf(`{"questions":[{"prompt":"Q","options":[{"label":"%s"},{"label":"b"}]}]}`,
			strings.Repeat("x", 41)), "option labels must be 1 to 40 characters"},
		{"long description", fmt.Sprintf(`{"questions":[{"prompt":"Q","options":[{"label":"a","description":"%s"},{"label":"b"}]}]}`,
			strings.Repeat("x", 201)), "option descriptions must be at most 200 characters"},
		{"long context", fmt.Sprintf(`{"context":"%s","questions":[{"prompt":"Q","options":[{"label":"a"},{"label":"b"}]}]}`,
			strings.Repeat("x", 1201)), "context is too long (max 1200)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			client := testClient(t, func(http.ResponseWriter, *http.Request) { hit = true }, &recorded{})
			text := toolErrorText(t, callOne(t, client, "ask", tc.args))
			if !strings.Contains(text, tc.want) {
				t.Errorf("error = %q, want it to contain %q", text, tc.want)
			}
			if hit {
				t.Error("invalid arguments must not reach multica-be")
			}
		})
	}
}

func TestPlanCreateWriteSubmit(t *testing.T) {
	cases := []struct {
		name       string
		args       string
		wantMethod string
		wantPath   string
		wantBody   map[string]any
		respond    string
	}{
		{
			name:       "create",
			args:       `{"action":"create","title":"Ship the lane","outline":["Why","How"]}`,
			wantMethod: "POST",
			wantPath:   "/api/workspaces/" + testWorkspaceID + "/tasks/" + testTaskID + "/plans",
			wantBody:   map[string]any{"title": "Ship the lane", "outline": []any{"Why", "How"}},
			respond:    `{"id":"plan-1","issue_id":"iss-1","title":"Ship the lane","status":"draft","revision":1}`,
		},
		{
			name:       "write",
			args:       fmt.Sprintf(`{"action":"write","plan_id":"%s","base_revision":3,"section_id":"s2","heading":"How","markdown":"Steps."}`, testPlanID),
			wantMethod: "PUT",
			wantPath:   "/api/workspaces/" + testWorkspaceID + "/plans/" + testPlanID + "/sections",
			wantBody: map[string]any{
				"base_revision": float64(3),
				"section":       map[string]any{"section_id": "s2", "heading": "How", "markdown": "Steps."},
			},
			respond: `{"id":"plan-1","issue_id":"iss-1","title":"t","status":"draft","revision":4}`,
		},
		{
			name:       "submit",
			args:       fmt.Sprintf(`{"action":"submit","plan_id":"%s","revision":3,"note":"Ready."}`, testPlanID),
			wantMethod: "POST",
			wantPath:   "/api/workspaces/" + testWorkspaceID + "/plans/" + testPlanID + "/submit",
			wantBody:   map[string]any{"revision": float64(3), "note": "Ready."},
			respond:    `{"id":"plan-1","issue_id":"iss-1","title":"t","status":"in_review","revision":3}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec recorded
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tc.respond)
			}, &rec)
			text := toolText(t, callOne(t, client, "plan", tc.args))
			if rec.method != tc.wantMethod || rec.path != tc.wantPath {
				t.Errorf("got %s %s, want %s %s", rec.method, rec.path, tc.wantMethod, tc.wantPath)
			}
			if rec.auth != "Bearer "+testToken {
				t.Errorf("auth = %q", rec.auth)
			}
			var sent map[string]any
			if err := json.Unmarshal(rec.body, &sent); err != nil {
				t.Fatalf("sent body: %v", err)
			}
			if fmt.Sprint(sent) != fmt.Sprint(tc.wantBody) {
				t.Errorf("body = %v, want %v", sent, tc.wantBody)
			}
			if !strings.Contains(text, "plan-1") {
				t.Errorf("success text should carry the plan id: %s", text)
			}
		})
	}
}

func TestPlanSubmitTextCarriesParkNotice(t *testing.T) {
	client := testClient(t, okHandler(`{"id":"plan-1","issue_id":"i","title":"t","status":"in_review","revision":2}`), nil)
	text := toolText(t, callOne(t, client, "plan",
		fmt.Sprintf(`{"action":"submit","plan_id":"%s","revision":2}`, testPlanID)))
	if !strings.Contains(text, "blocked awaiting a human verdict") {
		t.Errorf("submit must tell the agent the task parks: %s", text)
	}
}

// TestPlanStaleRevisionCarries409Body is the Architect's note 3: a stale
// fence must surface multica-be's current_revision/current_status VERBATIM so
// the model can self-correct without a read action.
func TestPlanStaleRevisionCarries409Body(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"stale_plan_revision","current_revision":7,"current_status":"changes_requested"}`)
	}, nil)
	text := toolErrorText(t, callOne(t, client, "plan",
		fmt.Sprintf(`{"action":"write","plan_id":"%s","base_revision":3,"heading":"H","markdown":"M"}`, testPlanID)))
	for _, want := range []string{"HTTP 409", "stale_plan_revision", `"current_revision":7`, `"current_status":"changes_requested"`} {
		if !strings.Contains(text, want) {
			t.Errorf("tool error must carry %q verbatim: %s", want, text)
		}
	}
}

func TestPlanActionErrorsStayLocal(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"no action", `{}`, "action is required"},
		{"bad action", `{"action":"delete"}`, "action must be create, write or submit"},
		{"create no title", `{"action":"create"}`, "title is required"},
		{"create long title", fmt.Sprintf(`{"action":"create","title":"%s"}`, strings.Repeat("x", 201)), "title is required and must be at most 200"},
		{"create big outline", fmt.Sprintf(`{"action":"create","title":"t","outline":[%s]}`, strings.TrimSuffix(strings.Repeat(`"s",`, 41), ",")), "outline has too many sections"},
		{"write no plan id", `{"action":"write","heading":"H","markdown":"M"}`, "plan_id is required for write"},
		{"write no revision", `{"action":"write","plan_id":"p","heading":"H","markdown":"M"}`, "base_revision is required"},
		{"write no content", `{"action":"write","plan_id":"p","base_revision":1}`, "heading or markdown is required"},
		{"write huge section", fmt.Sprintf(`{"action":"write","plan_id":"p","base_revision":1,"markdown":"%s"}`, strings.Repeat("x", 20001)), "section body is too large"},
		{"submit no plan id", `{"action":"submit","revision":1}`, "plan_id is required for submit"},
		{"submit no revision", fmt.Sprintf(`{"action":"submit","plan_id":"%s"}`, testPlanID), "revision is required for submit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			client := testClient(t, func(http.ResponseWriter, *http.Request) { hit = true }, &recorded{})
			text := toolErrorText(t, callOne(t, client, "plan", tc.args))
			if !strings.Contains(text, tc.want) {
				t.Errorf("error = %q, want it to contain %q", text, tc.want)
			}
			if hit {
				t.Error("invalid arguments must not reach multica-be")
			}
		})
	}
}

// TestUnconfiguredServesToolErrors covers the daemon's degradation contract:
// a server started without a usable client or scope must answer every call
// with a tool error and stay alive (an exit fails the ACP/dsh session).
func TestUnconfiguredServesToolErrors(t *testing.T) {
	cases := []struct {
		name   string
		client *APIClient
		scope  Scope
		want   string
	}{
		{"nil client", nil, testScope(), "not configured for this run"},
		{"incomplete scope", testClient(t, okHandler(`{}`), nil), Scope{}, "MULTICA_WORKSPACE_ID and MULTICA_TASK_ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range []string{"ask", "plan"} {
				args := `{"questions":[{"prompt":"Q","options":[{"label":"a"},{"label":"b"}]}]}`
				if tool == "plan" {
					args = `{"action":"create","title":"t"}`
				}
				text := toolErrorText(t, callOneIn(t, tc.client, tc.scope, tool, args))
				if !strings.Contains(text, tc.want) {
					t.Errorf("%s error = %q, want %q", tool, text, tc.want)
				}
			}
		})
	}
}

func TestProtocolErrors(t *testing.T) {
	client := testClient(t, okHandler(`{}`), nil)

	frames := drive(t, client, testScope(), `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != -32601 {
		t.Fatalf("unsupported method: want -32601, got %+v", frames)
	}

	frames = drive(t, client, testScope(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_search","arguments":{}}}`)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != -32602 {
		t.Fatalf("unknown tool: want -32602, got %+v", frames)
	}

	frames = drive(t, client, testScope(), `{not json`)
	if len(frames) != 1 || frames[0].Error == nil || frames[0].Error.Code != -32700 {
		t.Fatalf("bad frame: want -32700, got %+v", frames)
	}

	frames = drive(t, client, testScope(),
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`)
	if len(frames) != 1 || frames[0].Error != nil {
		t.Fatalf("ping should be the only reply: %+v", frames)
	}
}

// TestNewAPIClientRejectsBadConfiguration pins what the daemon and the
// subcommand consider unusable — those skips are what keep a broken entry
// out of a session that would fail on its handshake.
func TestNewAPIClientRejectsBadConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		token  string
		wantEr bool
	}{
		{"ok", "http://127.0.0.1:8080", testToken, false},
		{"ok trailing slash", "http://127.0.0.1:8080/", testToken, false},
		{"empty base", "", testToken, true},
		{"ws scheme", "ws://127.0.0.1:8080", testToken, true},
		{"not a url", "://nope", testToken, true},
		{"empty token", "http://127.0.0.1:8080", "", true},
		{"blank token", "http://127.0.0.1:8080", "   ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewAPIClient(tc.base, tc.token)
			if tc.wantEr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatal("expected a client")
			}
		})
	}
}
