package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseDshModelID(t *testing.T) {
	got, err := parseDshModelID("deepseek-official/deepseek-v4%2Fflash")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "deepseek-official" || got.ID != "deepseek-v4/flash" {
		t.Fatalf("unexpected model: %#v", got)
	}
	if _, err := parseDshModelID("deepseek-v4-flash"); err == nil {
		t.Fatal("unqualified DSH model should fail")
	}
}

func TestBuildDshMCPServers(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"files":{"command":"node","args":["server.js"],"env":{"TOKEN":"value"}},"remote":{"type":"streamable-http","url":"https://mcp.example/rpc","headers":{"Authorization":"Bearer test"}}}}`)
	got, err := buildDshMCPServers(raw, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d MCP servers", len(got))
	}
	if got[0].Transport != "stdio" || got[0].Command != "node" || got[0].Env["TOKEN"] != "value" {
		t.Fatalf("bad stdio server: %#v", got[0])
	}
	if got[1].Transport != "streamable-http" || got[1].Headers["Authorization"] != "Bearer test" {
		t.Fatalf("bad HTTP server: %#v", got[1])
	}
}

func TestBuildDshMCPServersRejectsSSE(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"legacy":{"type":"sse","url":"https://mcp.example/sse"}}}`)
	if _, err := buildDshMCPServers(raw, slog.Default()); err == nil || !strings.Contains(err.Error(), "SSE") {
		t.Fatalf("expected SSE error, got %v", err)
	}
}

func TestDshBackendExecuteStreamsProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
if [ "$1" != "--profile" ] || [ "$2" != "multica" ] || [ "$3" != "--stdio" ]; then exit 9; fi
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r command
case "$command" in *'"type":"execute"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"session","request_id":"task-1","session_id":"session-1","resumed":false}'
printf '%s\n' '{"v":1,"type":"thinking","request_id":"task-1","content":"checking"}'
printf '%s\n' '{"v":1,"type":"tool_call","request_id":"task-1","call_id":"call-1","name":"bash","arguments":"{\"command\":\"pwd\"}"}'
printf '%s\n' '{"v":1,"type":"tool_result","request_id":"task-1","call_id":"call-1","name":"bash","output":"/work","is_error":false}'
printf '%s\n' '{"v":1,"type":"text","request_id":"task-1","content":"done"}'
printf '%s\n' '{"v":1,"type":"usage","request_id":"task-1","provider":"deepseek-official","model":"deepseek-v4-flash","input_tokens":12,"output_tokens":3,"cache_read_tokens":2}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-1","status":"completed","session_id":"session-1","output":"done","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-1", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "say done", ExecOptions{Cwd: t.TempDir(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var messages []Message
	for message := range session.Messages {
		messages = append(messages, message)
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "done" || result.SessionID != "session-1" {
		t.Fatalf("bad result: %#v", result)
	}
	usage := result.Usage["deepseek-official/deepseek-v4-flash"]
	if usage.InputTokens != 12 || usage.OutputTokens != 3 || usage.CacheReadTokens != 2 {
		t.Fatalf("bad usage: %#v", usage)
	}
	if len(messages) != 5 || messages[0].SessionID != "session-1" || messages[2].Tool != "bash" {
		t.Fatalf("bad messages: %#v", messages)
	}
}

func TestDshBackendCancellationUsesProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r execute
printf '%s\n' '{"v":1,"type":"session","request_id":"task-cancel","session_id":"session-cancel","resumed":false}'
IFS= read -r cancel
case "$cancel" in *'"type":"cancel"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"result","request_id":"task-cancel","status":"cancelled","session_id":"session-cancel","output":"","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-cancel", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := b.Execute(ctx, "wait", ExecOptions{Cwd: t.TempDir(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	message := <-session.Messages
	if message.SessionID != "session-cancel" {
		t.Fatalf("bad session message: %#v", message)
	}
	cancel()
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "cancelled" || result.SessionID != "session-cancel" {
		t.Fatalf("bad cancellation result: %#v", result)
	}
}

func TestDiscoverDshModels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"models","models":[{"id":"deepseek-official/deepseek-v4-flash","label":"DeepSeek V4 Flash","provider":"DeepSeek","default":true,"thinking":{"supported_levels":[{"value":"high","label":"High"},{"value":"max","label":"Max"}],"default_level":"high"}}]}'
`)
	models, err := discoverDshModels(context.Background(), Command{Path: bin})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || !models[0].Default || models[0].Thinking == nil || models[0].Thinking.DefaultLevel != "high" {
		t.Fatalf("bad models: %#v", models)
	}
}

func writeDshFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dsh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDshBackendQuestionAnswerRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{"questions":true}}'
IFS= read -r execute
case "$execute" in *'"type":"execute"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"session","request_id":"task-q","session_id":"session-q","resumed":false}'
printf '%s\n' '{"v":1,"type":"question","request_id":"task-q","call_id":"q-1","questions":[{"id":"plan","question":"Approve this plan and leave plan mode?","header":"Plan review","detail":"# Plan\n1. do the thing","options":[{"label":"Approve"},{"label":"Keep planning"}],"intent":{"kind":"plan-review","approve":"Approve","callId":"c9"}}]}'
IFS= read -r answer
printf '%s\n' "$answer" | grep -q '"type":"answer"' || exit 7
printf '%s\n' "$answer" | grep -q '"request_id":"task-q"' || exit 7
printf '%s\n' "$answer" | grep -q '"call_id":"q-1"' || exit 7
printf '%s\n' "$answer" | grep -q '"id":"plan"' || exit 7
printf '%s\n' "$answer" | grep -q '"selected":\["Approve"\]' || exit 7
printf '%s\n' '{"v":1,"type":"tool_result","request_id":"task-q","call_id":"c9","name":"exit_plan_mode","output":"approved","is_error":false}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-q","status":"completed","session_id":"session-q","output":"done","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-q", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "plan it", ExecOptions{Cwd: t.TempDir(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var supplementErr error
	supplementCalls := 0
	var questionContent string
	var questionInput map[string]any
	for message := range session.Messages {
		if message.Type != MessageQuestion {
			continue
		}
		questionContent = message.Content
		questionInput = message.Input
		if message.CallID != "q-1" {
			t.Fatalf("question call id = %q, want q-1", message.CallID)
		}
		if !session.SupplementReady() {
			t.Fatal("SupplementReady false while the question is parked")
		}
		supplementCalls++
		supplementErr = session.Supplement(context.Background(),
			"[ADDITIONAL GUIDANCE] Human \"Alice\" added guidance while you were working.\n\nHuman message:\nApprove", "steer")
	}
	if supplementCalls != 1 {
		t.Fatalf("question messages = %d, want 1", supplementCalls)
	}
	if supplementErr != nil {
		t.Fatalf("Supplement: %v", supplementErr)
	}
	for _, want := range []string{"Plan review", "Approve this plan", "# Plan", "reply *Approve*"} {
		if !strings.Contains(questionContent, want) {
			t.Fatalf("question transcript missing %q:\n%s", want, questionContent)
		}
	}
	// The structured half: the client builds the answer form from this, so the
	// option labels, the plan detail and the approval label all have to survive
	// the trip in a JSON-decoded shape.
	encoded, err := json.Marshal(questionInput)
	if err != nil {
		t.Fatalf("encode question input: %v", err)
	}
	for _, want := range []string{
		`"questions":[`, `"id":"plan"`, `"header":"Plan review"`,
		`"detail":"# Plan\n1. do the thing"`, `"label":"Approve"`, `"label":"Keep planning"`,
		`"intent":{"approve":"Approve","kind":"plan-review"}`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("question input missing %q:\n%s", want, encoded)
		}
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("bad result: %#v", result)
	}
	if err := session.Supplement(context.Background(), "again", "steer"); err == nil || !strings.Contains(err.Error(), "no question is pending") {
		t.Fatalf("second Supplement should report no pending question, got %v", err)
	}
}

func TestDshBackendSupplementRefusedWithoutQuestionsCapability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	// An old bridge: ready frame without the questions capability. Its stdin
	// parser rejects unknown command types, so the daemon must refuse the
	// supplement without writing anything — the fixture completes only when
	// no extra stdin line arrives.
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r execute
printf '%s\n' '{"v":1,"type":"session","request_id":"task-old","session_id":"session-old","resumed":false}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-old","status":"completed","session_id":"session-old","output":"done","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-old", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "hello", ExecOptions{Cwd: t.TempDir(), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	supplementErr := make(chan error, 1)
	for message := range session.Messages {
		if message.Type == MessageStatus {
			if !session.SupplementReady() {
				t.Fatal("SupplementReady false after the session frame")
			}
			go func() {
				supplementErr <- session.Supplement(context.Background(), "answer me", "steer")
			}()
		}
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("run should complete without the answer line being written: %#v", result)
	}
	if err := <-supplementErr; err == nil || !strings.Contains(err.Error(), "questions") {
		t.Fatalf("Supplement should refuse a bridge without the questions capability, got %v", err)
	}
}

func TestAnswerDshQuestionMappings(t *testing.T) {
	var sent []dshAnswerCommand
	state := &dshRunState{
		requestID:        "task-1",
		questionsCapable: true,
		writeAnswer: func(command dshAnswerCommand) error {
			sent = append(sent, command)
			return nil
		},
	}
	if err := answerDshQuestion(state, "Human message:\nanything"); err == nil || !strings.Contains(err.Error(), "no question is pending") {
		t.Fatalf("expected no-pending error, got %v", err)
	}

	state.pendingCallID = "q-1"
	state.pendingQuestions = []dshQuestion{
		{ID: "plan", Question: "Approve?", Intent: &dshQuestionIntent{Kind: "plan-review", Approve: "Approve"}},
	}
	// The supplement framing must be stripped; only the member's word "approve"
	// (case-insensitively matching the approve label) selects the option.
	if err := answerDshQuestion(state, "[ADDITIONAL GUIDANCE] Human \"Bob\" added guidance.\n\nHuman message:\napprove"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("answers sent = %d, want 1", len(sent))
	}
	command := sent[0]
	if command.Version != DshProtocolVersion || command.Type != "answer" || command.RequestID != "task-1" || command.CallID != "q-1" {
		t.Fatalf("bad answer command: %#v", command)
	}
	if len(command.Answers) != 1 || command.Answers[0].ID != "plan" ||
		len(command.Answers[0].Selected) != 1 || command.Answers[0].Selected[0] != "Approve" ||
		command.Answers[0].Custom != "" {
		t.Fatalf("approve label should map to the approval selection: %#v", command.Answers[0])
	}
	if state.pendingCallID != "" {
		t.Fatal("pending question should clear after a delivered answer")
	}

	// Any other reply is free-text feedback, delivered as custom text.
	state.pendingCallID = "q-2"
	state.pendingQuestions = []dshQuestion{
		{ID: "plan", Question: "Approve?", Intent: &dshQuestionIntent{Kind: "plan-review", Approve: "Approve"}},
		{ID: "mode", Question: "Which mode?"},
	}
	if err := answerDshQuestion(state, "use the blue deployment"); err != nil {
		t.Fatal(err)
	}
	command = sent[1]
	if len(command.Answers) != 2 || command.Answers[0].Custom != "use the blue deployment" ||
		len(command.Answers[0].Selected) != 0 || command.Answers[1].Custom != "use the blue deployment" {
		t.Fatalf("free-text replies should answer every question as custom text: %#v", command.Answers)
	}

	// A bridge that predates the capability is refused before any write.
	refused := &dshRunState{pendingCallID: "q-3"}
	if err := answerDshQuestion(refused, "Human message:\nhi"); err == nil || !strings.Contains(err.Error(), "questions") {
		t.Fatalf("expected capability refusal, got %v", err)
	}
}

func TestRenderDshQuestions(t *testing.T) {
	got := renderDshQuestions([]dshQuestion{{
		ID: "q1", Question: "Ship it?", Header: "Confirm",
		Options: []dshQuestionOption{{Label: "Yes", Description: "Ship now"}, {Label: "No"}},
	}})
	for _, want := range []string{"Confirm", "Ship it?", "- Yes — Ship now", "- No"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendering missing %q:\n%s", want, got)
		}
	}
	plan := renderDshQuestions([]dshQuestion{{
		ID: "p", Question: "Approve this plan?", Detail: "# Plan\n1. step",
		Intent: &dshQuestionIntent{Kind: "plan-review", Approve: "Approve"},
	}})
	for _, want := range []string{"📋 Plan review", "reply *Approve*"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan rendering missing %q:\n%s", want, plan)
		}
	}
}

func TestDshSupplementContentStripsFraming(t *testing.T) {
	framed := "[ADDITIONAL GUIDANCE] Human \"Alice\" added guidance while you were working.\n\n- Preserve and complete the original objective.\n\nHuman message:\nuse staging"
	if got := dshSupplementContent(framed); got != "use staging" {
		t.Fatalf("content = %q, want %q", got, "use staging")
	}
	if got := dshSupplementContent("raw reply"); got != "raw reply" {
		t.Fatalf("unframed content = %q", got)
	}
}
