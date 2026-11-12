// Package moctmcp is the `multica moct-mcp` stdio MCP server: the on-demand
// half of the MOCT seam (docs/moct-seam.md out-path 2, M-101).
//
// It exposes three tools — memory_search, memory_get, memory_note — as thin
// wrappers over server/internal/moct, the package that owns every HTTP call to
// moct-be. moct-be stays the only writer to the memory store; this server holds
// no state, no handle and no cache.
//
// Scope is passed as ENVIRONMENT, never as tool arguments (MOCT_WORKSPACE_ID,
// MOCT_EXPERT_ID, MOCT_TASK_REF, MOCT_CHANNEL_REF). A run therefore cannot
// express a request outside its own workspace, agent profile and issue — the
// tool schemas do not even carry those fields.
//
// Failure rule, inherited from the seam: a moct-be that is down, slow or
// answering 4xx/5xx produces a TOOL error the agent reads and works around,
// never a protocol error and never a non-zero exit. The daemon starts this
// server from inside a task, and an ACP/dsh runtime fails the whole session
// when a stdio MCP server dies (server/pkg/agent/acp_session.go) — so dying is
// exactly the behaviour that would make memory block a task.
package moctmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/moct"
)

const (
	// ProtocolVersion is the MCP revision this server speaks, matching the
	// version Multica's own MCP servers already answer with.
	ProtocolVersion = "2024-11-05"
	// ServerName is the name initialize reports; the daemon mounts this server
	// under the same name in the task's MCP config.
	ServerName = "moct"
	// ServerVersion identifies this server's tool surface to a connected
	// client; bumped by hand when a tool's shape changes.
	ServerVersion = "1"
	// maxRequestBytes caps one JSON-RPC frame. A memory_note body is the only
	// legitimately large input, and the client's own response cap is 1 MiB, so
	// this bounds a runaway the same way.
	maxRequestBytes = 1 << 20
	// callTimeout bounds one tool call, matching the inject path's budget plus
	// room for a write that has to wait on moct-be's duplicate check.
	callTimeout = 60 * time.Second
)

// Scope is the env-derived identity every tool call operates inside. All four
// values are D2 refs (multica:ws:…, multica:agent:…, multica:issue:…,
// multica:project:…); WorkspaceRef and ExpertRef are required by moct-be, the
// other two are optional buckets.
type Scope struct {
	WorkspaceRef string
	ExpertRef    string
	TaskRef      string
	ChannelRef   string
}

// Complete reports whether this scope can make any call at all. moct-be answers
// 400 to a read without workspaceId/expertId, so an incomplete scope is a
// configuration problem surfaced as a tool error rather than a crash loop.
func (s Scope) Complete() bool {
	return s.WorkspaceRef != "" && s.ExpertRef != ""
}

// readerScope widens the scope refs into the OR-ed tag filter every read takes.
func (s Scope) readerScope() moct.Scope {
	scope := moct.Scope{WorkspaceID: s.WorkspaceRef, ExpertID: s.ExpertRef}
	if s.ChannelRef != "" {
		scope.Channels = []string{s.ChannelRef}
	}
	if s.TaskRef != "" {
		scope.Tasks = []string{s.TaskRef}
	}
	return scope
}

// Server serves the three memory tools over stdio. A nil client is legal and
// makes every call fail as a tool error — that is the "moct-be is not
// configured/reachable on this host" shape, and it keeps the process alive.
type Server struct {
	client *moct.Client
	scope  Scope
	logger *slog.Logger
}

// New builds a server. client may be nil; scope may be incomplete. Both are
// reported per call rather than at construction, because a server that exits
// at startup takes the surrounding session with it.
func New(client *moct.Client, scope Scope, logger *slog.Logger) *Server {
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
		return fmt.Errorf("moct-mcp: encode response: %w", err)
	}
	if _, err := out.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("moct-mcp: write response: %w", err)
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
		payload any
		err     error
	)
	switch call.Name {
	case "memory_search":
		payload, err = s.search(ctx, args)
	case "memory_get":
		payload, err = s.get(ctx, args)
	case "memory_note":
		payload, err = s.note(ctx, args)
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool " + call.Name}
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Info("moct-mcp: tool call failed", "tool", call.Name, "error", err)
		}
		return toolError(err), nil
	}
	text, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return toolError(marshalErr), nil
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}}, nil
}

// toolError renders a failure as a tool RESULT. The agent reads it, sees the
// call did not work, and carries on with the issue — which is the whole point
// of the seam's failure mode.
func toolError(err error) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": err.Error()}},
	}
}

// notConfigured is the per-call failure for a server started without a usable
// client or scope. It is an error and not an exit for the reason in the package
// doc: this process must outlive its own misconfiguration.
func (s *Server) notConfigured() error {
	if s.client == nil {
		return errors.New("moct memory is not configured on this host: no moct-be base URL")
	}
	if !s.scope.Complete() {
		return errors.New("moct memory is not configured for this run: workspace and expert refs are required")
	}
	return nil
}

func (s *Server) search(ctx context.Context, args json.RawMessage) (any, error) {
	if err := s.notConfigured(); err != nil {
		return nil, err
	}
	var in struct {
		Query  string   `json:"q"`
		Types  []string `json:"types"`
		Homes  []string `json:"homes"`
		Status string   `json:"status"`
		Limit  int      `json:"limit"`
		Cursor string   `json:"cursor"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("memory_search: bad arguments: %w", err)
	}
	return s.client.Search(ctx, moct.SearchParams{
		Scope:  s.scope.readerScope(),
		Query:  in.Query,
		Types:  in.Types,
		Homes:  in.Homes,
		Status: in.Status,
		Limit:  in.Limit,
		Cursor: in.Cursor,
	})
}

func (s *Server) get(ctx context.Context, args json.RawMessage) (any, error) {
	if err := s.notConfigured(); err != nil {
		return nil, err
	}
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("memory_get: bad arguments: %w", err)
	}
	// The client enforces 1..10 ids; its error reads as a tool error and the
	// agent retries with a legal batch. Ids outside this scope come back in
	// GetResult.Missing, never as data.
	return s.client.Get(ctx, in.IDs, s.scope.readerScope())
}

func (s *Server) note(ctx context.Context, args json.RawMessage) (any, error) {
	if err := s.notConfigured(); err != nil {
		return nil, err
	}
	var in struct {
		Type       string         `json:"type"`
		Title      string         `json:"title"`
		Body       string         `json:"body"`
		Attrs      map[string]any `json:"attrs"`
		Home       *moct.Home     `json:"home"`
		ConfirmNew bool           `json:"confirmNew"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("memory_note: bad arguments: %w", err)
	}
	home, err := s.noteHome(in.Home)
	if err != nil {
		return nil, err
	}
	return s.client.Note(ctx, moct.NoteParams{
		Type:  in.Type,
		Title: in.Title,
		Body:  in.Body,
		Attrs: in.Attrs,
		Home:  home,
		// From env, never from arguments: the record is written into this
		// run's workspace and attributed to this run's knowledge profile.
		WorkspaceID: s.scope.WorkspaceRef,
		Actor:       moct.Actor{Kind: "member", ID: s.scope.ExpertRef},
		ConfirmNew:  in.ConfirmNew,
	})
}

// noteHome is the write's home: the caller's, or the task home of this run.
// A run that is not attached to an issue has no default home and must name one.
func (s *Server) noteHome(explicit *moct.Home) (moct.Home, error) {
	if explicit != nil {
		if explicit.Kind == "" || explicit.Ref == "" {
			return moct.Home{}, errors.New("memory_note: home needs both kind and ref")
		}
		return *explicit, nil
	}
	if s.scope.TaskRef == "" {
		return moct.Home{}, errors.New("memory_note: this run has no task ref; pass home explicitly ({kind, ref})")
	}
	return moct.Home{Kind: "task", Ref: s.scope.TaskRef}, nil
}
