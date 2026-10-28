package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	dshProfile = "multica"
	// DshProtocolVersion is the DSH stdio protocol this backend speaks. Exported
	// because the daemon's profile probe accepts or rejects a runtime profile on
	// this exact number, and a second copy of it there is a copy that can drift.
	DshProtocolVersion = 1
	dshCancelGrace     = 3 * time.Second
	dshTerminateGrace  = 2 * time.Second
)

// dshBackend drives the Multica DSH bundle over its versioned JSONL
// stdio protocol. The adapter is intentionally independent of ACP: DSH owns
// the agent loop, session store, model catalog, tools, and MCP clients, while
// this package only translates those events into Multica's Backend contract.
type dshBackend struct {
	cfg Config
}

type dshModelSelection struct {
	Provider        string `json:"provider"`
	ID              string `json:"id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type dshMCPServer struct {
	Name              string            `json:"name"`
	Transport         string            `json:"transport"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Cwd               string            `json:"cwd,omitempty"`
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	ToolCallTimeoutMS int               `json:"tool_call_timeout_ms,omitempty"`
}

type dshExecuteCommand struct {
	Version         int                `json:"v"`
	Type            string             `json:"type"`
	RequestID       string             `json:"request_id"`
	Cwd             string             `json:"cwd"`
	Prompt          string             `json:"prompt"`
	ResumeSessionID string             `json:"resume_session_id,omitempty"`
	Model           *dshModelSelection `json:"model,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	MCPServers      []dshMCPServer     `json:"mcp_servers"`
}

type dshCancelCommand struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

type dshSteerCommand struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

type dshQueueCommand struct {
	Version   int    `json:"v"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Message   string `json:"message"`
}

type dshWireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// dshQuestionOption is one selectable answer offered by an ask/plan question.
type dshQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// dshQuestionIntent is the plan-review presentation intent the bridge passes
// through from dsh's user-questions seam: a question tagged `plan-review` IS
// a plan submission, `Approve` names the option that approves it, and the
// question's detail carries the plan markdown (M-109).
type dshQuestionIntent struct {
	Kind    string `json:"kind"`
	Approve string `json:"approve,omitempty"`
	CallID  string `json:"callId,omitempty"`
}

// dshQuestion is one question of an outbound `question` frame — the wire
// shape mirrors dsh's AskUserQuestionItem (deepseek-harness
// packages/interaction/user-questions).
type dshQuestion struct {
	ID          string              `json:"id"`
	Question    string              `json:"question"`
	Detail      string              `json:"detail,omitempty"`
	Header      string              `json:"header,omitempty"`
	Options     []dshQuestionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multiSelect,omitempty"`
	Intent      *dshQuestionIntent  `json:"intent,omitempty"`
}

// dshCapabilitiesFrame is the capability advertisement on the bridge's
// `ready` frame. Only `questions` is read today; the rest of the bridge's
// advertisement (resume, cancel, models, …) is implicit in protocol v1.
type dshCapabilitiesFrame struct {
	Questions bool `json:"questions,omitempty"`
	Steer     bool `json:"steer,omitempty"`
	Queue     bool `json:"queue,omitempty"`
}

// dshAnswerItem is one question's answer in an outbound `answer` command.
// `Selected` carries chosen option labels; `Custom` carries free text (the
// human's own words when they did not pick an offered option).
type dshAnswerItem struct {
	ID       string   `json:"id"`
	Selected []string `json:"selected"`
	Custom   string   `json:"custom,omitempty"`
}

// dshAnswerCommand is the daemon→bridge reply that resolves a parked ask.
// Additive at protocol v1: a bridge that predates it never emits `question`,
// so this is only ever written after the bridge advertised `questions`.
type dshAnswerCommand struct {
	Version   int             `json:"v"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	CallID    string          `json:"call_id"`
	Answers   []dshAnswerItem `json:"answers"`
}

type dshFrame struct {
	Version          int                   `json:"v"`
	Type             string                `json:"type"`
	Runtime          string                `json:"runtime,omitempty"`
	ProtocolVersion  int                   `json:"protocol_version,omitempty"`
	RequestID        string                `json:"request_id,omitempty"`
	SessionID        string                `json:"session_id,omitempty"`
	Resumed          bool                  `json:"resumed,omitempty"`
	Content          string                `json:"content,omitempty"`
	CallID           string                `json:"call_id,omitempty"`
	Name             string                `json:"name,omitempty"`
	Arguments        string                `json:"arguments,omitempty"`
	Output           string                `json:"output,omitempty"`
	IsError          bool                  `json:"is_error,omitempty"`
	Provider         string                `json:"provider,omitempty"`
	Model            string                `json:"model,omitempty"`
	InputTokens      int64                 `json:"input_tokens,omitempty"`
	OutputTokens     int64                 `json:"output_tokens,omitempty"`
	CacheReadTokens  int64                 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64                 `json:"cache_write_tokens,omitempty"`
	Status           string                `json:"status,omitempty"`
	StopReason       string                `json:"stop_reason,omitempty"`
	ResumeRejected   bool                  `json:"resume_rejected,omitempty"`
	Error            *dshWireError         `json:"error,omitempty"`
	Code             string                `json:"code,omitempty"`
	Message          string                `json:"message,omitempty"`
	Models           []dshModelFrame       `json:"models,omitempty"`
	Questions        []dshQuestion         `json:"questions,omitempty"`
	Capabilities     *dshCapabilitiesFrame `json:"capabilities,omitempty"`
	State            string                `json:"state,omitempty"`
	Command          string                `json:"command,omitempty"`
	Reason           string                `json:"reason,omitempty"`
}

type dshThinkingFrame struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	DefaultLevel    string          `json:"default_level,omitempty"`
}

type dshModelFrame struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Provider string            `json:"provider,omitempty"`
	Default  bool              `json:"default,omitempty"`
	Thinking *dshThinkingFrame `json:"thinking,omitempty"`
}

func dshLaunchArgs() []string {
	return []string{"--profile", dshProfile, "--stdio"}
}

func parseDshModelID(value string) (*dshModelSelection, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	providerPart, modelPart, ok := strings.Cut(value, "/")
	if !ok || providerPart == "" || modelPart == "" {
		return nil, fmt.Errorf("dsh model must use the provider/model ID advertised by DSH")
	}
	provider, err := url.PathUnescape(providerPart)
	if err != nil {
		return nil, fmt.Errorf("decode dsh model provider: %w", err)
	}
	if strings.TrimSpace(provider) == "" {
		return nil, errors.New("dsh model provider is empty")
	}
	model, err := url.PathUnescape(modelPart)
	if err != nil {
		return nil, fmt.Errorf("decode dsh model ID: %w", err)
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("dsh model ID is empty")
	}
	return &dshModelSelection{Provider: provider, ID: model}, nil
}

func buildDshMCPServers(raw json.RawMessage, logger interface {
	Warn(string, ...any)
}) ([]dshMCPServer, error) {
	servers, err := buildACPMcpServers(raw, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dshMCPServer, 0, len(servers))
	for _, rawServer := range servers {
		server, ok := rawServer.(map[string]any)
		if !ok {
			continue
		}
		name, _ := server["name"].(string)
		if command, _ := server["command"].(string); command != "" {
			entry := dshMCPServer{Name: name, Transport: "stdio", Command: command, Args: []string{}, Env: map[string]string{}}
			if args, ok := server["args"].([]string); ok {
				entry.Args = args
			}
			if pairs, ok := server["env"].([]map[string]any); ok {
				for _, pair := range pairs {
					key, _ := pair["name"].(string)
					value, _ := pair["value"].(string)
					if key != "" {
						entry.Env[key] = value
					}
				}
			}
			out = append(out, entry)
			continue
		}

		transport, _ := server["type"].(string)
		if transport == "sse" {
			return nil, fmt.Errorf("dsh MCP server %q uses SSE, but the DSH runtime supports stdio and streamable HTTP only", name)
		}
		remoteURL, _ := server["url"].(string)
		if remoteURL == "" {
			if logger != nil {
				logger.Warn("skipping invalid DSH MCP server", "name", name)
			}
			continue
		}
		entry := dshMCPServer{Name: name, Transport: "streamable-http", URL: remoteURL, Headers: map[string]string{}}
		if pairs, ok := server["headers"].([]map[string]any); ok {
			for _, pair := range pairs {
				key, _ := pair["name"].(string)
				value, _ := pair["value"].(string)
				if key != "" {
					entry.Headers[key] = value
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (b *dshBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "dsh"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("dsh executable not found at %q: %w", execPath, err)
	}
	model, err := parseDshModelID(opts.Model)
	if err != nil {
		return nil, err
	}
	mcpServers, err := buildDshMCPServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("dsh: invalid mcp_config: %w", err)
	}

	runCtx, cancel := runContext(ctx, opts.Timeout)
	args := dshLaunchArgs()
	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)
	if b.cfg.TaskID != "" {
		cmd.Env = append(cmd.Env, "DSH_SESSION_OWNER=Multica run "+b.cfg.TaskID)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh stdin pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[dsh:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start dsh: %w", err)
	}

	requestID := b.cfg.TaskID
	if requestID == "" {
		requestID = "multica-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	state := dshRunState{usage: make(map[string]TokenUsage), requestID: requestID, inboxStateCh: make(chan dshInboxState, 1)}
	command := dshExecuteCommand{
		Version: DshProtocolVersion, Type: "execute", RequestID: requestID,
		Cwd: opts.Cwd, Prompt: prompt, ResumeSessionID: opts.ResumeSessionID,
		Model: model, ReasoningEffort: opts.ThinkingLevel, MCPServers: mcpServers,
	}
	var writeMu sync.Mutex
	encoder := json.NewEncoder(stdin)
	writeFrame := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(value)
	}
	state.writeAnswer = func(command dshAnswerCommand) error { return writeFrame(command) }
	state.writeCommand = writeFrame
	if err := writeFrame(command); err != nil {
		signalProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
		releaseProcessGroup(cmd)
		cancel()
		return nil, fmt.Errorf("send dsh execute command: %w", err)
	}

	b.cfg.Logger.Info("dsh started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)
	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	procDone := make(chan struct{})

	go func() {
		select {
		case <-procDone:
			return
		case <-runCtx.Done():
		}
		_ = writeFrame(dshCancelCommand{Version: DshProtocolVersion, Type: "cancel", RequestID: requestID})
		timer := time.NewTimer(dshCancelGrace)
		defer timer.Stop()
		select {
		case <-procDone:
			return
		case <-timer.C:
		}
		signalProcessGroup(cmd, syscall.SIGTERM)
		if !waitProcessGroupGone(cmd, dshTerminateGrace) {
			signalProcessGroup(cmd, syscall.SIGKILL)
		}
		_ = stdout.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer func() { _ = stdin.Close() }()
		started := time.Now()
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var frame dshFrame
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				state.invalidFrames++
				continue
			}
			state.frameCount++
			handleDshFrame(frame, requestID, msgCh, &state)
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			_ = stdout.Close()
		}
		exitErr := cmd.Wait()
		close(procDone)
		releaseProcessGroup(cmd)

		result := state.result
		if result == nil {
			result = &Result{Status: "failed", SessionID: state.sessionID, Usage: state.usage}
			switch {
			case errors.Is(runCtx.Err(), context.DeadlineExceeded):
				result.Status = "timeout"
				result.Error = fmt.Sprintf("dsh timed out after %s", opts.Timeout)
			case errors.Is(runCtx.Err(), context.Canceled):
				result.Status = "cancelled"
				result.Error = "dsh execution cancelled"
			case scanErr != nil:
				result.Error = fmt.Sprintf("read dsh event stream: %v", scanErr)
			case state.protocolError != "":
				result.Error = state.protocolError
			case !state.ready:
				result.Error = "dsh exited before the runtime protocol became ready"
			case exitErr != nil:
				result.Error = fmt.Sprintf("dsh exited with error: %v", exitErr)
			default:
				result.Error = "dsh exited without a terminal result"
			}
		}
		if result.Error != "" {
			result.Error = withAgentStderr(result.Error, "dsh", stderrBuf.Tail())
		}
		result.DurationMs = time.Since(started).Milliseconds()
		b.cfg.Logger.Info("dsh finished", "pid", cmd.Process.Pid, "status", result.Status,
			"duration", time.Since(started).Round(time.Millisecond).String(), "frames", state.frameCount,
			"invalid_frames", state.invalidFrames)
		resCh <- *result
	}()

	return &Session{
		Messages: msgCh,
		Result:   resCh,
		// Ask/plan parity (M-109) + steer/queue (M-135): the daemon's
		// supplement loop delivers the human's reply here. A parked question
		// is answered; otherwise the message is steered (next step) or queued
		// (next turn) per the supplement's mode. SupplementReady is true once
		// the bridge confirmed the agent session.
		Supplement: func(ctx context.Context, instruction string, mode string) error {
			return dshSupplement(&state, instruction, mode, ctx)
		},
		SupplementReady: state.live.Load,
	}, nil
}

type dshRunState struct {
	ready         bool
	sessionID     string
	protocolError string
	frameCount    int
	invalidFrames int
	usage         map[string]TokenUsage
	result        *Result

	// Ask/plan parity state (M-109). The frame handler (scanner goroutine)
	// writes it; Session.Supplement reads and clears it from the daemon's
	// supplement goroutine, hence the mutex. `live` is set once the bridge
	// reports the agent session and backs SupplementReady.
	mu               sync.Mutex
	requestID        string
	questionsCapable bool
	steerCapable     bool
	queueCapable     bool
	pendingCallID    string
	pendingQuestions []dshQuestion
	writeAnswer      func(dshAnswerCommand) error
	writeCommand     func(any) error
	live             atomic.Bool
	// inbox_state correlation (M-134 amendment). A buffered channel carries the
	// most recent matching frame; pendingInbox names the command currently
	// awaiting an ack so stale frames are ignored.
	inboxStateCh    chan dshInboxState
	pendingInbox   string
	inboxStartedAt time.Time
}

type dshInboxState struct {
	Command string
	State   string
	Reason  string
}

func handleDshFrame(frame dshFrame, requestID string, ch chan<- Message, state *dshRunState) {
	if frame.Version != DshProtocolVersion {
		state.protocolError = fmt.Sprintf("dsh returned unsupported protocol version %d", frame.Version)
		return
	}
	if frame.RequestID != "" && frame.RequestID != requestID {
		return
	}
	switch frame.Type {
	case "ready":
		state.ready = frame.Runtime == "dsh"
		state.mu.Lock()
		if frame.Capabilities != nil {
			state.questionsCapable = frame.Capabilities.Questions
			state.steerCapable = frame.Capabilities.Steer
			state.queueCapable = frame.Capabilities.Queue
		} else {
			state.questionsCapable = false
			state.steerCapable = false
			state.queueCapable = false
		}
		state.mu.Unlock()
	case "inbox_state":
		state.mu.Lock()
		pending := state.pendingInbox
		started := state.inboxStartedAt
		ch := state.inboxStateCh
		state.mu.Unlock()
		if pending != "" && ch != nil && frame.Command == pending && !time.Now().Before(started) {
			select {
			case ch <- dshInboxState{Command: frame.Command, State: frame.State, Reason: frame.Reason}:
			default:
			}
		}
	case "session":
		state.sessionID = frame.SessionID
		state.live.Store(true)
		trySend(ch, Message{Type: MessageStatus, Status: "running", SessionID: frame.SessionID})
	case "text":
		if frame.Content != "" {
			trySend(ch, Message{Type: MessageText, Content: frame.Content})
		}
	case "thinking":
		if frame.Content != "" {
			trySend(ch, Message{Type: MessageThinking, Content: frame.Content})
		}
	case "tool_call":
		input := map[string]any{}
		if frame.Arguments != "" && json.Unmarshal([]byte(frame.Arguments), &input) != nil {
			input = map[string]any{"raw": frame.Arguments}
		}
		trySend(ch, Message{Type: MessageToolUse, Tool: frame.Name, CallID: frame.CallID, Input: input})
	case "tool_result":
		trySend(ch, Message{Type: MessageToolResult, Tool: frame.Name, CallID: frame.CallID, Output: frame.Output})
	case "question":
		// Ask/plan parity (M-109): the bridge parked the run on a user
		// question (ask_user_question, or exit_plan_mode's plan review).
		// Record it so Supplement can correlate the human's answer, and
		// surface the rendered questions on the transcript.
		if len(frame.Questions) == 0 {
			break
		}
		callID := frame.CallID
		if callID == "" {
			callID = "question-" + strconv.Itoa(state.frameCount)
		}
		state.mu.Lock()
		state.pendingCallID = callID
		state.pendingQuestions = append([]dshQuestion(nil), frame.Questions...)
		state.mu.Unlock()
		trySend(ch, Message{
			Type:    MessageQuestion,
			CallID:  callID,
			Content: renderDshQuestions(frame.Questions),
			Input:   dshQuestionInput(frame.Questions),
		})
	case "usage":
		key := frame.Model
		if frame.Provider != "" {
			key = frame.Provider + "/" + frame.Model
		}
		if key != "" {
			usage := state.usage[key]
			usage.InputTokens += frame.InputTokens
			usage.OutputTokens += frame.OutputTokens
			usage.CacheReadTokens += frame.CacheReadTokens
			usage.CacheWriteTokens += frame.CacheWriteTokens
			state.usage[key] = usage
		}
	case "protocol_error":
		state.protocolError = strings.TrimSpace(frame.Code + ": " + frame.Message)
	case "result":
		errorText := ""
		if frame.Error != nil {
			errorText = strings.TrimSpace(frame.Error.Code + ": " + frame.Error.Message)
		}
		state.result = &Result{
			Status: frame.Status, Output: frame.Output, Error: errorText,
			SessionID: frame.SessionID, Usage: state.usage, ResumeRejected: frame.ResumeRejected,
		}
		if state.result.SessionID == "" {
			state.result.SessionID = state.sessionID
		}
	}
}

// dshQuestionInput is the structured half of a question row: the same list the
// bridge sent, in a shape a client can build a form from.
//
// The row carries both halves and needs them both. `content` stays the human
// rendering, so a client that does not know this payload — or a mirror that
// stores nothing but text — still shows the member the question and its
// options. `input.questions` is what lets the fork's Inbox render the ask as
// the card the member answers in place instead of retyping a reply. Lives
// under the same `input` key tool calls use, so the transcript row needs no new
// column.
//
// Keys are snake_case (multi_select) to match the server-side JSON conventions
// the client parses; the bridge's camelCase is flattened here, at the one
// boundary that owns the translation.
func dshQuestionInput(questions []dshQuestion) map[string]any {
	list := make([]any, 0, len(questions))
	for _, question := range questions {
		options := make([]any, 0, len(question.Options))
		for _, option := range question.Options {
			entry := map[string]any{"label": option.Label}
			if option.Description != "" {
				entry["description"] = option.Description
			}
			options = append(options, entry)
		}
		item := map[string]any{
			"id":       question.ID,
			"question": question.Question,
			"options":  options,
		}
		if question.Header != "" {
			item["header"] = question.Header
		}
		if question.Detail != "" {
			item["detail"] = question.Detail
		}
		if question.MultiSelect {
			item["multi_select"] = true
		}
		if question.Intent != nil {
			intent := map[string]any{"kind": question.Intent.Kind}
			if question.Intent.Approve != "" {
				intent["approve"] = question.Intent.Approve
			}
			item["intent"] = intent
		}
		list = append(list, item)
	}
	return map[string]any{"questions": list}
}

// renderDshQuestions renders parked questions for the transcript line the
// daemon posts — the member answers by commenting on the issue addressed to
// the agent, and the daemon steers that reply back through Supplement.
func renderDshQuestions(questions []dshQuestion) string {
	var b strings.Builder
	b.WriteString("❓ The agent needs your answer — reply to this issue mentioning the agent.\n")
	for i, question := range questions {
		fmt.Fprintf(&b, "\n**Q%d**", i+1)
		if header := strings.TrimSpace(question.Header); header != "" {
			fmt.Fprintf(&b, " (%s)", header)
		}
		fmt.Fprintf(&b, ": %s\n", strings.TrimSpace(question.Question))
		if detail := strings.TrimSpace(question.Detail); detail != "" {
			b.WriteString(detail)
			b.WriteByte('\n')
		}
		for _, option := range question.Options {
			if description := strings.TrimSpace(option.Description); description != "" {
				fmt.Fprintf(&b, "- %s — %s\n", option.Label, description)
			} else {
				fmt.Fprintf(&b, "- %s\n", option.Label)
			}
		}
		if intent := question.Intent; intent != nil && intent.Kind == "plan-review" && intent.Approve != "" {
			fmt.Fprintf(&b, "📋 Plan review — reply *%s* to approve the plan above, or reply with your feedback to keep planning.\n", intent.Approve)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// dshSupplementMessageMarker precedes the human's own words in the framed
// instruction formatTaskSupplementInstruction builds. The framing exists for
// ordinary steering; an answer must carry the member's words, not the frame.
const dshSupplementMessageMarker = "Human message:\n"

func dshSupplementContent(instruction string) string {
	if _, after, ok := strings.Cut(instruction, dshSupplementMessageMarker); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(instruction)
}

// dshSupplement delivers a human message to the bridge as an answer (when a
// question is parked), a steer (next step) or a queue (next turn), per mode.
func dshSupplement(state *dshRunState, instruction, mode string, ctx context.Context) error {
	if mode == "" {
		mode = "steer"
	}
	state.mu.Lock()
	if state.pendingCallID != "" {
		state.mu.Unlock()
		return answerDshQuestion(state, instruction)
	}
	var capable bool
	if mode == "queue" {
		capable = state.queueCapable
	} else {
		mode = "steer"
		capable = state.steerCapable
	}
	if !capable {
		questionsCapable := state.questionsCapable
		steerCapable := state.steerCapable
		queueCapable := state.queueCapable
		state.mu.Unlock()
		if !questionsCapable && !steerCapable && !queueCapable {
			return errors.New("dsh runtime bridge predates question answers (no `questions` capability in its ready frame)")
		}
		return errors.New("no question is pending on this dsh run")
	}
	state.inboxStartedAt = time.Now()
	// Drain stale frames from any previous command.
	for len(state.inboxStateCh) > 0 {
		<-state.inboxStateCh
	}
	state.pendingInbox = mode
	state.mu.Unlock()

	content := dshSupplementContent(instruction)
	var cmd any
	if mode == "queue" {
		cmd = dshQueueCommand{Version: DshProtocolVersion, Type: "queue", RequestID: state.requestID, Message: content}
	} else {
		cmd = dshSteerCommand{Version: DshProtocolVersion, Type: "steer", RequestID: state.requestID, Message: content}
	}
	if state.writeCommand == nil {
		state.mu.Lock()
		state.pendingInbox = ""
		state.mu.Unlock()
		return errors.New("dsh command writer is unavailable")
	}
	if err := state.writeCommand(cmd); err != nil {
		state.mu.Lock()
		state.pendingInbox = ""
		state.mu.Unlock()
		return err
	}
	err := waitForDshInboxState(state, mode, ctx)
	state.mu.Lock()
	state.pendingInbox = ""
	state.mu.Unlock()
	return err
}

func waitForDshInboxState(state *dshRunState, command string, ctx context.Context) error {
	state.mu.Lock()
	ch := state.inboxStateCh
	state.mu.Unlock()
	if ch == nil {
		return nil
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case is := <-ch:
			if is.Command != "" && is.Command != command {
				continue
			}
			switch is.State {
			case "rejected":
				if strings.TrimSpace(is.Reason) != "" {
					return errors.New(strings.TrimSpace(is.Reason))
				}
				return errors.New("dsh rejected the supplement")
			default:
				return nil
			}
		case <-timer.C:
			return nil
		}
	}
}

// answerDshQuestion turns one steered human comment into the `answer` command
// that resolves the bridge's parked ask. Free text answers every question as
// custom text — the one deliberate mapping is a plan review answered with
// exactly its approve label, which dsh treats as the approval selection.
func answerDshQuestion(state *dshRunState, instruction string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.questionsCapable {
		return errors.New("dsh runtime bridge predates question answers (no `questions` capability in its ready frame)")
	}
	if state.pendingCallID == "" {
		return errors.New("no question is pending on this dsh run")
	}
	if state.writeAnswer == nil {
		return errors.New("dsh answer writer is unavailable")
	}
	content := dshSupplementContent(instruction)
	answers := make([]dshAnswerItem, 0, len(state.pendingQuestions))
	for _, question := range state.pendingQuestions {
		item := dshAnswerItem{ID: question.ID, Selected: []string{}, Custom: content}
		if intent := question.Intent; intent != nil && intent.Kind == "plan-review" && intent.Approve != "" &&
			strings.EqualFold(content, intent.Approve) {
			item = dshAnswerItem{ID: question.ID, Selected: []string{intent.Approve}}
		}
		answers = append(answers, item)
	}
	err := state.writeAnswer(dshAnswerCommand{
		Version: DshProtocolVersion, Type: "answer", RequestID: state.requestID,
		CallID: state.pendingCallID, Answers: answers,
	})
	if err != nil {
		return fmt.Errorf("send dsh answer command: %w", err)
	}
	state.pendingCallID = ""
	state.pendingQuestions = nil
	return nil
}

func discoverDshModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "dsh"
	}
	cmd := runtimeCmd.exec(ctx, "--profile", dshProfile, "--list-models")
	hideAgentWindow(cmd)
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := startOwnedProcessTree(cmd, runtimeCmd.logger); err != nil {
		return nil, err
	}
	var models []Model
	scanner := newAgentStreamScanner(stdout)
	for scanner.Scan() {
		var frame dshFrame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Version != DshProtocolVersion || frame.Type != "models" {
			continue
		}
		for _, item := range frame.Models {
			model := Model{ID: item.ID, Label: item.Label, Provider: item.Provider, Default: item.Default}
			if item.Thinking != nil && len(item.Thinking.SupportedLevels) > 0 {
				model.Thinking = &ModelThinking{SupportedLevels: item.Thinking.SupportedLevels, DefaultLevel: item.Thinking.DefaultLevel}
			}
			models = append(models, model)
		}
	}
	scanErr := scanner.Err()
	exitErr := cmd.Wait()
	releaseProcessGroup(cmd)
	if scanErr != nil {
		return nil, scanErr
	}
	if exitErr != nil {
		return nil, exitErr
	}
	if len(models) == 0 {
		return nil, errors.New("dsh returned an empty model catalog")
	}
	return models, nil
}
