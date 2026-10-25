package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubAsker answers with a canned AskAnswer and records the request it saw.
type stubAsker struct {
	answer AskAnswer
	err    error

	got     AskRequest
	calls   int
	release chan struct{} // when non-nil, Ask blocks until it is closed
}

func (s *stubAsker) Ask(ctx context.Context, req AskRequest) (AskAnswer, error) {
	s.calls++
	s.got = req
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return AskAnswer{Declined: true, Reason: "run cancelled"}, ctx.Err()
		}
	}
	return s.answer, s.err
}

// readAskFixture replays the recorded CodeBuddy frames and returns the first
// can_use_tool control_request for AskUserQuestion.
//
// Provenance: the frames are transcribed from @tencent-ai/codebuddy-code's
// stream-json transport (SdkPermissionClient.requestPermission and the
// AskUserQuestion tool's input schema) rather than captured from a live run —
// the freebuff account this workspace has could not complete a paid turn. The
// shapes are therefore the contract the CLI's own code enforces, which is what
// the backend depends on; a live capture should replace this file when one is
// available.
func readAskFixture(t *testing.T) (codebuddySDKMessage, []map[string]any) {
	t.Helper()

	f, err := os.Open("testdata/codebuddy-askuserquestion.stream-json.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var askMsg codebuddySDKMessage
	var questions []map[string]any
	seen := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg codebuddySDKMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("fixture line is not a stream-json frame: %v", err)
		}
		if msg.Type != "control_request" {
			continue
		}
		var req codebuddyControlRequestPayload
		if err := json.Unmarshal(msg.Request, &req); err != nil {
			t.Fatalf("fixture control_request is malformed: %v", err)
		}
		if req.ToolName != codebuddyAskToolName {
			continue
		}
		var input codebuddyAskInput
		if err := json.Unmarshal(req.Input, &input); err != nil {
			t.Fatalf("fixture ask input is malformed: %v", err)
		}
		for _, q := range input.Questions {
			questions = append(questions, map[string]any{"question": q.Question, "header": q.Header})
		}
		askMsg = msg
		seen++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if seen != 1 {
		t.Fatalf("expected exactly one AskUserQuestion control_request in the fixture, got %d", seen)
	}
	return askMsg, questions
}

func decodeControlResponse(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &resp); err != nil {
		t.Fatalf("unmarshal control response %q: %v", raw, err)
	}
	inner, ok := resp["response"].(map[string]any)
	if !ok {
		t.Fatalf("response envelope missing: %v", resp)
	}
	return inner
}

// The answer must ride the permission response as updatedInput.answers: the CLI
// refuses an allow without answers ("stream-json allowed AskUserQuestion
// without answers; ignoring") and the tool reads input.answers before it ever
// waits for one.
func TestCodebuddyAskControlRequestAnswersWithUpdatedInput(t *testing.T) {
	t.Parallel()

	msg, questions := readAskFixture(t)

	var req codebuddyControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	asker := &stubAsker{answer: AskAnswer{Answers: map[string]string{
		"Which database should the question table live in?": "Postgres",
		"Which columns must the first slice cover?":         "Full",
	}}}

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	b.handleAskControlRequest(context.Background(), msg.RequestID, req, &codebuddyStdin{w: &written}, asker)

	if asker.calls != 1 {
		t.Fatalf("expected one ask, got %d", asker.calls)
	}
	if len(asker.got.Questions) != len(questions) {
		t.Fatalf("asker saw %d questions, fixture has %d", len(asker.got.Questions), len(questions))
	}
	if asker.got.ToolCallID != "toolu_ask_1" {
		t.Fatalf("asker saw tool call id %q, want toolu_ask_1", asker.got.ToolCallID)
	}
	if got := asker.got.Questions[0].Options; len(got) != 2 || got[0].Label != "Postgres" {
		t.Fatalf("options were not carried through: %+v", got)
	}
	if !asker.got.Questions[1].MultiSelect {
		t.Fatal("multiSelect was not carried through")
	}

	inner := decodeControlResponse(t, written.Bytes())
	if inner["subtype"] != "success" || inner["request_id"] != msg.RequestID {
		t.Fatalf("unexpected envelope: %v", inner)
	}
	body, ok := inner["response"].(map[string]any)
	if !ok {
		t.Fatalf("response body missing: %v", inner)
	}
	if body["allowed"] != true {
		t.Fatalf("expected allowed=true, got %v", body["allowed"])
	}
	updated, ok := body["updatedInput"].(map[string]any)
	if !ok {
		t.Fatalf("updatedInput missing: %v", body)
	}
	answers, ok := updated["answers"].(map[string]any)
	if !ok {
		t.Fatalf("answers missing from updatedInput: %v", updated)
	}
	if answers["Which database should the question table live in?"] != "Postgres" {
		t.Fatalf("answer not keyed by question text: %v", answers)
	}
	// The original input must survive: the CLI replaces the tool input with
	// this object, so dropping `questions` would erase what was asked.
	if _, ok := updated["questions"]; !ok {
		t.Fatalf("updatedInput dropped the original questions: %v", updated)
	}
}

// An expired or cancelled ask must still unblock the call, and it must do so as
// a successful tool result telling the model to continue — not as a permission
// denial, which surfaces as a tool error the model may retry.
func TestCodebuddyAskControlRequestSkipsQuestionWhenDeclined(t *testing.T) {
	t.Parallel()

	msg, _ := readAskFixture(t)
	var req codebuddyControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	asker := &stubAsker{answer: AskAnswer{Declined: true, Reason: "no answer received in time; proceed with your best assumption or end the turn"}}
	b.handleAskControlRequest(context.Background(), msg.RequestID, req, &codebuddyStdin{w: &written}, asker)

	inner := decodeControlResponse(t, written.Bytes())
	body := inner["response"].(map[string]any)
	if body["allowed"] != true {
		t.Fatalf("a declined ask must still be allowed so the tool can report the skip, got %v", body)
	}
	updated := body["updatedInput"].(map[string]any)
	answers := updated["answers"].(map[string]any)
	if answers["_action"] != codebuddySkipQuestionAction {
		t.Fatalf("expected the provider's skip action, got %v", answers)
	}
}

// An asker error is a decline, not a crash: the run keeps going.
func TestCodebuddyAskControlRequestTreatsAskerErrorAsDecline(t *testing.T) {
	t.Parallel()

	msg, _ := readAskFixture(t)
	var req codebuddyControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	asker := &stubAsker{err: errors.New("server unreachable")}
	b.handleAskControlRequest(context.Background(), msg.RequestID, req, &codebuddyStdin{w: &written}, asker)

	inner := decodeControlResponse(t, written.Bytes())
	body := inner["response"].(map[string]any)
	answers := body["updatedInput"].(map[string]any)["answers"].(map[string]any)
	if answers["_action"] != codebuddySkipQuestionAction {
		t.Fatalf("expected the provider's skip action, got %v", answers)
	}
}

// A cancelled run must not write a response at all: the pipe is being closed,
// and a late frame would race the teardown.
func TestCodebuddyAskControlRequestWritesNothingAfterCancellation(t *testing.T) {
	t.Parallel()

	msg, _ := readAskFixture(t)
	var req codebuddyControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	asker := &stubAsker{answer: AskAnswer{Answers: map[string]string{"Which database should the question table live in?": "Postgres"}}}
	b.handleAskControlRequest(ctx, msg.RequestID, req, &codebuddyStdin{w: &written}, asker)

	if written.Len() != 0 {
		t.Fatalf("expected no response after cancellation, got %q", written.String())
	}
}

// The read loop must not block on an open question: it has to keep draining
// stdout or the CLI deadlocks writing into a full pipe.
func TestCodebuddyHandleControlRequestDoesNotBlockOnAsk(t *testing.T) {
	t.Parallel()

	msg, _ := readAskFixture(t)

	asker := &stubAsker{
		answer:  AskAnswer{Answers: map[string]string{"Which database should the question table live in?": "Postgres"}},
		release: make(chan struct{}),
	}

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written syncBuffer
	stdin := &codebuddyStdin{w: &written}

	done := make(chan struct{})
	go func() {
		b.handleControlRequest(msg, stdin, asker, context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleControlRequest blocked on the ask instead of dispatching it")
	}

	// No response yet: the question is still open.
	if written.String() != "" {
		t.Fatalf("expected no response while the ask is open, got %q", written.String())
	}

	close(asker.release)
	deadline := time.Now().Add(2 * time.Second)
	for written.String() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if written.String() == "" {
		t.Fatal("expected an answer once the asker returned")
	}
}

// The handshake is what makes the CLI expose the tool at all: without
// capabilities.askUserQuestion it reports "stream-json peer did not declare
// askUserQuestion capability" and AskUserQuestion never reaches the model.
func TestWriteCodebuddyInitializeDeclaresAskCapability(t *testing.T) {
	t.Parallel()

	var written bytes.Buffer
	if err := writeCodebuddyInitialize(&codebuddyStdin{w: &written}); err != nil {
		t.Fatalf("writeCodebuddyInitialize: %v", err)
	}

	var frame map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &frame); err != nil {
		t.Fatalf("unmarshal initialize frame: %v", err)
	}
	if frame["type"] != "control_request" {
		t.Fatalf("initialize must be a control_request, got %v", frame["type"])
	}
	if frame["request_id"] != codebuddyInitializeRequestID {
		t.Fatalf("unexpected request id: %v", frame["request_id"])
	}
	req := frame["request"].(map[string]any)
	if req["subtype"] != "initialize" {
		t.Fatalf("unexpected subtype: %v", req["subtype"])
	}
	if req["hasPrompt"] != true {
		t.Fatal("hasPrompt must be true so the CLI keeps the process alive for the prompt")
	}
	caps := req["capabilities"].(map[string]any)
	if caps["askUserQuestion"] != true {
		t.Fatalf("askUserQuestion capability not declared: %v", caps)
	}
}

// A malformed ask (no questions) cannot become a question record, and an allow
// without answers is rejected by the CLI, so it is denied with a reason.
func TestCodebuddyAskControlRequestDeniesMalformedInput(t *testing.T) {
	t.Parallel()

	b := &codebuddyBackend{cfg: Config{Logger: slog.Default()}}
	var written bytes.Buffer
	req := codebuddyControlRequestPayload{
		Subtype:   "can_use_tool",
		ToolName:  codebuddyAskToolName,
		ToolUseID: "toolu_bad",
		Input:     json.RawMessage(`{"questions":[]}`),
	}
	asker := &stubAsker{}
	b.handleAskControlRequest(context.Background(), "perm_bad", req, &codebuddyStdin{w: &written}, asker)

	if asker.calls != 0 {
		t.Fatal("a malformed ask must not reach the answer channel")
	}
	body := decodeControlResponse(t, written.Bytes())["response"].(map[string]any)
	if body["allowed"] != false {
		t.Fatalf("expected a denial, got %v", body)
	}
	reason, _ := body["reason"].(string)
	if reason == "" {
		t.Fatal("a denial must carry a reason the model can act on")
	}
	for _, synthetic := range []string{"cancelled", "timeout", "prompt ended", "unattended"} {
		if strings.HasPrefix(strings.ToLower(reason), synthetic) {
			t.Fatalf("reason %q starts with a phrase CodeBuddy swallows as a synthetic deny", reason)
		}
	}
}

// The unit tests above drive handleAskControlRequest directly. This one drives
// the whole transport with a stand-in CLI: the real Execute path, a real stdin
// pipe, a real read loop. It is the only test that can catch an ordering bug —
// the capability frame must precede the prompt, and the answer must arrive on
// stdin as a control_response the CLI can match to its request_id.
func TestCodebuddyAskEndToEndWithFakeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	dir := t.TempDir()
	fakePath := filepath.Join(dir, "codebuddy")
	// The fake CLI records the two frames it is sent before the ask, emits a
	// permission request for AskUserQuestion, then records the answer frame it
	// receives and finishes the turn.
	script := `#!/bin/sh
out="$FAKE_CB_OUT"
IFS= read -r init_frame
printf '%s\n' "$init_frame" > "$out.stdin"
IFS= read -r prompt_frame
printf '%s\n' "$prompt_frame" >> "$out.stdin"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-ask"}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","model":"m","content":[{"type":"text","text":"I need a decision."}]}}'
printf '%s\n' '{"type":"control_request","request_id":"perm_int_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which database?","header":"Database","options":[{"label":"Postgres"},{"label":"SQLite"}],"multiSelect":false}]},"tool_use_id":"toolu_int_1"}}'
IFS= read -r answer_frame
printf '%s\n' "$answer_frame" > "$out.response"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-ask","result":"done"}'
`
	writeTestExecutable(t, fakePath, []byte(script))
	outBase := filepath.Join(dir, "frames")
	t.Setenv("FAKE_CB_OUT", outBase)

	b := &codebuddyBackend{cfg: Config{ExecutablePath: fakePath, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	asker := &stubAsker{answer: AskAnswer{Answers: map[string]string{"Which database?": "Postgres"}}}
	session, err := b.Execute(ctx, "do the thing", ExecOptions{Timeout: 15 * time.Second, Ask: asker})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for range session.Messages {
	}
	result, ok := <-session.Result
	if !ok {
		t.Fatal("result channel closed without a value")
	}
	if result.Status != "completed" {
		t.Fatalf("status = %q, want completed (error=%q)", result.Status, result.Error)
	}
	if asker.calls != 1 {
		t.Fatalf("asker calls = %d, want 1", asker.calls)
	}
	if asker.got.ToolCallID != "toolu_int_1" {
		t.Fatalf("asker saw tool call id %q", asker.got.ToolCallID)
	}

	// Ordering on stdin: capability frame, then prompt. A prompt written
	// first would reach a CLI that has not enabled the tool yet.
	stdinLines := readFrameLines(t, outBase+".stdin")
	if len(stdinLines) != 2 {
		t.Fatalf("expected initialize + prompt on stdin, got %d frames: %v", len(stdinLines), stdinLines)
	}
	var initFrame map[string]any
	if err := json.Unmarshal([]byte(stdinLines[0]), &initFrame); err != nil {
		t.Fatalf("initialize frame is not JSON: %v", err)
	}
	if initFrame["type"] != "control_request" {
		t.Fatalf("first frame type = %v, want control_request", initFrame["type"])
	}
	initReq, _ := initFrame["request"].(map[string]any)
	if initReq["subtype"] != "initialize" {
		t.Fatalf("first frame subtype = %v, want initialize", initReq["subtype"])
	}
	caps, _ := initReq["capabilities"].(map[string]any)
	if caps["askUserQuestion"] != true {
		t.Fatalf("initialize did not declare askUserQuestion: %v", caps)
	}

	var promptFrame map[string]any
	if err := json.Unmarshal([]byte(stdinLines[1]), &promptFrame); err != nil {
		t.Fatalf("prompt frame is not JSON: %v", err)
	}
	if promptFrame["type"] != "user" {
		t.Fatalf("second frame type = %v, want user prompt", promptFrame["type"])
	}

	// The answer must be a control_response matched to the CLI's request_id,
	// carrying updatedInput.answers — the only shape the CLI accepts.
	responseLines := readFrameLines(t, outBase+".response")
	if len(responseLines) != 1 {
		t.Fatalf("expected one control_response, got %d: %v", len(responseLines), responseLines)
	}
	inner := decodeControlResponse(t, []byte(responseLines[0]))
	if inner["request_id"] != "perm_int_1" {
		t.Fatalf("response request_id = %v, want perm_int_1", inner["request_id"])
	}
	body, _ := inner["response"].(map[string]any)
	if body["allowed"] != true {
		t.Fatalf("expected allowed=true, got %v", body)
	}
	answers, _ := body["updatedInput"].(map[string]any)["answers"].(map[string]any)
	if answers["Which database?"] != "Postgres" {
		t.Fatalf("answers = %v, want the chosen option", answers)
	}
}

func readFrameLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recorded frames %s: %v", path, err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
