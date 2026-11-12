// Package askplanmcp is the `multica askplan-mcp` stdio MCP server: the
// agent-side delivery of S1-A2 (M-119).
//
// It exposes two tools — ask and plan — as thin wrappers over multica-be's
// S1-A1 routes (server/internal/handler/askplan.go, merged in PR #20). It
// talks to multica-be only; nothing in this package imports or reaches MOCT.
//
// Scope is passed as ENVIRONMENT, never as tool arguments
// (MULTICA_WORKSPACE_ID, MULTICA_TASK_ID, plus MULTICA_SERVER_URL and the
// mat_ MULTICA_TOKEN). A run therefore cannot express a call outside its own
// task: the tool schemas do not even carry workspace or task fields, and the
// server independently refuses anything but the caller's own running issue
// task.
//
// Non-blocking contract: a successful ask/submit answers immediately and the
// SERVER has already parked the task as blocked ("awaiting human"); the
// daemon interrupts the run, and the human's answer/verdict re-queues a run
// resuming the same session with the handoff rendered in its prompt. The park
// interrupt may cut the tool RESULT short — that is by design (S1-A1); the
// resumed prompt is the reliable channel.
//
// Failure rule, deliberately the opposite of the MOCT seam: memory must never
// block a task, but ask/plan is an explicit agent action, so a failure is a
// TOOL error the agent reads (validation messages, or multica-be's structured
// 409 body carried verbatim) — never a protocol error and never a non-zero
// exit. Dying would fail the ACP/dsh session that spawned us
// (server/pkg/agent/acp_session.go).
package askplanmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

const (
	// ProtocolVersion is the MCP revision this server speaks, matching the
	// version Multica's own MCP servers already answer with.
	ProtocolVersion = "2024-11-05"
	// ServerName is the name initialize reports; the daemon mounts this
	// server under the same name in the task's MCP config.
	ServerName = "multica"
	// ServerVersion identifies this server's tool surface to a connected
	// client; bumped by hand when a tool's shape changes.
	ServerVersion = "1"
	// maxRequestBytes caps one JSON-RPC frame. The largest legitimate input
	// is a plan section body (20000 chars ≈ well under 1 MiB with framing).
	maxRequestBytes = 1 << 20
	// callTimeout bounds one tool call; matches APIClient's own 60s plus
	// room so the client timeout fires first with the better message.
	callTimeout = 65 * time.Second
)

// Scope is the env-derived identity every tool call operates inside: the one
// workspace and the one task this server was started to serve.
type Scope struct {
	WorkspaceID string
	TaskID      string
}

// Complete reports whether this scope can make any call at all. Both values
// are required to build every route path, so an incomplete scope is a
// configuration problem surfaced per call rather than a crash at startup.
func (s Scope) Complete() bool {
	return s.WorkspaceID != "" && s.TaskID != ""
}

// Server serves the ask and plan tools over stdio. A nil client is legal and
// makes every call fail as a tool error — that is the "multica-be is not
// configured on this host" shape, and it keeps the process alive.
type Server struct {
	client *APIClient
	scope  Scope
	logger *slog.Logger
}

// New builds a server. client may be nil; scope may be incomplete. Both are
// reported per call rather than at construction, because a server that exits
// at startup takes the surrounding session with it.
func New(client *APIClient, scope Scope, logger *slog.Logger) *Server {
	return &Server{client: client, scope: scope, logger: logger}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run serves newline-delimited JSON-RPC frames on in until it reaches EOF,
// writing every frame to out. One request is handled at a time, so out needs
// no lock: the loop never writes a second frame before the first is done.
// Protocol output goes to out (stdout) only — the caller logs to stderr.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxRequestBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// No id to echo: the frame gave us nothing to answer with.
			if err := writeFrame(out, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: "request is not valid JSON-RPC"}}); err != nil {
				return err
			}
			continue
		}
		// A notification (no id) takes no reply, including for methods we do
		// not implement — that is the JSON-RPC rule, not a silent failure.
		if len(req.ID) == 0 {
			continue
		}
		result, rpcErr := s.handle(ctx, req)
		if result == nil && rpcErr == nil {
			continue
		}
		if err := writeFrame(out, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func writeFrame(out io.Writer, frame response) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("askplan-mcp: encode response: %w", err)
	}
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("askplan-mcp: write response: %w", err)
	}
	return nil
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
		}, nil
	case "notifications/initialized", "notifications/cancelled":
		// Answered by the id check in Run; listed so the method is not
		// reported as unsupported if a client sends it with an id anyway.
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDescriptors()}, nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "unsupported method " + req.Method}
	}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid tool call parameters"}
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var (
		text string
		err  error
	)
	switch call.Name {
	case "ask":
		text, err = s.ask(ctx, args)
	case "plan":
		text, err = s.plan(ctx, args)
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool " + call.Name}
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Info("askplan-mcp: tool call failed", "tool", call.Name, "error", err)
		}
		return toolError(err), nil
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil
}

// toolError renders a failure as a tool RESULT. The agent reads it, sees the
// call did not work, and decides — the task itself is untouched (no park
// happens on a failed call).
func toolError(err error) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
	}
}

// notConfigured is the per-call failure for a server started without a usable
// client or scope. It is an error and not an exit for the reason in the
// package doc: this process must outlive its own misconfiguration.
func (s *Server) notConfigured() error {
	if s.client == nil {
		return errors.New("ask/plan is not configured for this run: no usable multica-be base URL or task token (MULTICA_SERVER_URL / MULTICA_TOKEN)")
	}
	if !s.scope.Complete() {
		return errors.New("ask/plan is not configured for this run: MULTICA_WORKSPACE_ID and MULTICA_TASK_ID are required")
	}
	return nil
}

// ---- ask ----

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askQuestion struct {
	Prompt   string      `json:"prompt"`
	Multiple bool        `json:"multiple,omitempty"`
	Options  []askOption `json:"options"`
}

type askArgs struct {
	Context   string        `json:"context"`
	Questions []askQuestion `json:"questions"`
}

// ask validates, then POSTs the card to S1-A1's /tasks/{taskId}/ask.
func (s *Server) ask(ctx context.Context, args json.RawMessage) (string, error) {
	if err := s.notConfigured(); err != nil {
		return "", err
	}
	var in askArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("ask: bad arguments: %w", err)
	}
	if err := validateAsk(in); err != nil {
		return "", fmt.Errorf("ask: %w", err)
	}
	path := fmt.Sprintf("/api/workspaces/%s/tasks/%s/ask", s.scope.WorkspaceID, s.scope.TaskID)
	status, body, err := s.client.do(ctx, "POST", path, in)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", httpError(status, body)
	}
	var result struct {
		CardID  string `json:"card_id"`
		IssueID string `json:"issue_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("ask: unreadable answer from multica-be: %w", err)
	}
	return fmt.Sprintf(
		"Card %s is open for issue %s (status %s). This task is now blocked awaiting a human answer: stop working now, before any step no one can undo — the run resumes with the answer in its prompt.\n%s",
		result.CardID, result.IssueID, result.Status, string(body)), nil
}

// validateAsk mirrors validateAskRequest (handler/askplan.go:743) edge for
// edge, including the wording, so an agent sees one consistent message
// whether the check fired here or on the server — the numbers are the ones
// TestAskPlanMCPToolLimitsMatchHandler pins to the handler's constants.
func validateAsk(in askArgs) error {
	if len(in.Context) > AskContextLimit {
		return fmt.Errorf("context is too long (max %d)", AskContextLimit)
	}
	if len(in.Questions) == 0 || len(in.Questions) > AskMaxQuestions {
		return fmt.Errorf("questions must be 1 to %d", AskMaxQuestions)
	}
	for i, q := range in.Questions {
		if q.Prompt == "" || len(q.Prompt) > AskPromptLimit {
			return fmt.Errorf("question %d prompt must be 1 to %d characters", i+1, AskPromptLimit)
		}
		if len(q.Options) < AskMinOptions || len(q.Options) > AskMaxOptions {
			return fmt.Errorf("question %d must have %d to %d options", i+1, AskMinOptions, AskMaxOptions)
		}
		for _, o := range q.Options {
			if o.Label == "" || len(o.Label) > AskLabelLimit {
				return fmt.Errorf("option labels must be 1 to %d characters", AskLabelLimit)
			}
			if len(o.Description) > AskDescriptionLimit {
				return fmt.Errorf("option descriptions must be at most %d characters", AskDescriptionLimit)
			}
		}
	}
	return nil
}

// ---- plan ----

type planSection struct {
	SectionID string `json:"section_id,omitempty"`
	Heading   string `json:"heading"`
	Markdown  string `json:"markdown"`
}

type planArgs struct {
	Action       string      `json:"action"`
	Title        string      `json:"title"`
	Outline      []string    `json:"outline"`
	PlanID       string      `json:"plan_id"`
	BaseRevision *int32      `json:"base_revision"`
	Section      planSection `json:"section"`
	// SectionID/Heading/Markdown are the flat spellings the schema documents
	// (section_id, heading, markdown at the top level); Section is the
	// wire shape S1-A1's planWriteRequest nests under "section". The plan
	// builder accepts either spelling and always sends the nested one.
	SectionID string `json:"section_id"`
	Heading   string `json:"heading"`
	Markdown  string `json:"markdown"`
	Revision  *int32 `json:"revision"`
	Note      string `json:"note"`
}

type planResponse struct {
	ID       string `json:"id"`
	IssueID  string `json:"issue_id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Revision int32  `json:"revision"`
}

// plan dispatches one action onto S1-A1's plan routes. One action per call,
// like team-room's single team_plan tool: create → POST /tasks/{id}/plans,
// write → PUT /plans/{id}/sections, submit → POST /plans/{id}/submit.
func (s *Server) plan(ctx context.Context, args json.RawMessage) (string, error) {
	if err := s.notConfigured(); err != nil {
		return "", err
	}
	var in planArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("plan: bad arguments: %w", err)
	}
	switch in.Action {
	case "create":
		return s.planCreate(ctx, in)
	case "write":
		return s.planWrite(ctx, in)
	case "submit":
		return s.planSubmit(ctx, in)
	case "":
		return "", errors.New("plan: action is required (create, write or submit)")
	default:
		return "", fmt.Errorf("plan: action must be create, write or submit, not %q", in.Action)
	}
}

func (s *Server) planCreate(ctx context.Context, in planArgs) (string, error) {
	if in.Title == "" || len(in.Title) > PlanTitleLimit {
		return "", fmt.Errorf("plan: title is required and must be at most %d characters", PlanTitleLimit)
	}
	if len(in.Outline) > PlanMaxSections {
		return "", fmt.Errorf("plan: outline has too many sections (max %d)", PlanMaxSections)
	}
	path := fmt.Sprintf("/api/workspaces/%s/tasks/%s/plans", s.scope.WorkspaceID, s.scope.TaskID)
	body := map[string]any{"title": in.Title, "outline": in.Outline}
	result, raw, err := s.postPlan(ctx, "POST", path, body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Plan %s created for issue %s (status %s, revision %d). Write one section per call with action=write, fencing each write with the revision from the previous response; submit when ready.\n%s",
		result.ID, result.IssueID, result.Status, result.Revision, raw), nil
}

func (s *Server) planWrite(ctx context.Context, in planArgs) (string, error) {
	// Accept both the flat and the pre-nested spelling of the section.
	section := in.Section
	if section.Heading == "" && section.Markdown == "" {
		section = planSection{SectionID: in.SectionID, Heading: in.Heading, Markdown: in.Markdown}
	}
	if section.Heading == "" && section.Markdown == "" {
		return "", errors.New("plan: heading or markdown is required")
	}
	if len(section.Markdown) > PlanSectionCharMax {
		return "", fmt.Errorf("plan: section body is too large (max %d characters)", PlanSectionCharMax)
	}
	if in.PlanID == "" {
		return "", errors.New("plan: plan_id is required for write")
	}
	if in.BaseRevision == nil {
		return "", errors.New("plan: base_revision is required for write — fence with the revision your latest response rendered")
	}
	path := fmt.Sprintf("/api/workspaces/%s/plans/%s/sections", s.scope.WorkspaceID, in.PlanID)
	body := map[string]any{
		"base_revision": *in.BaseRevision,
		"section":       section,
	}
	result, raw, err := s.postPlan(ctx, "PUT", path, body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Section written; plan %s is now at revision %d (status %s).\n%s",
		result.ID, result.Revision, result.Status, raw), nil
}

func (s *Server) planSubmit(ctx context.Context, in planArgs) (string, error) {
	if in.PlanID == "" {
		return "", errors.New("plan: plan_id is required for submit")
	}
	if in.Revision == nil {
		return "", errors.New("plan: revision is required for submit — submit the revision your latest write rendered")
	}
	path := fmt.Sprintf("/api/workspaces/%s/plans/%s/submit", s.scope.WorkspaceID, in.PlanID)
	body := map[string]any{"revision": *in.Revision, "note": in.Note}
	result, raw, err := s.postPlan(ctx, "POST", path, body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Plan %s revision %d submitted for review (status %s). This task is now blocked awaiting a human verdict: stop working now, before any step no one can undo — the verdict (approved / changes requested with comments / cancel) arrives in the resumed run's prompt.\n%s",
		result.ID, result.Revision, result.Status, raw), nil
}

// postPlan performs one plan round trip and decodes the planResponse every
// plan route answers with, keeping the raw JSON for the agent (it carries
// the sections, so an appended section's assigned id is visible). Non-2xx
// becomes the verbatim-body tool error.
func (s *Server) postPlan(ctx context.Context, method, path string, body any) (planResponse, string, error) {
	status, raw, err := s.client.do(ctx, method, path, body)
	if err != nil {
		return planResponse{}, "", err
	}
	if status < 200 || status >= 300 {
		return planResponse{}, "", httpError(status, raw)
	}
	var result planResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return planResponse{}, "", fmt.Errorf("plan: unreadable answer from multica-be: %w", err)
	}
	return result, string(raw), nil
}
