package daemon

// Memory seam dispatch (docs/powercontext-seam.md).
//
// The daemon speaks to ONE memory backend per host. This file is the switch:
// it decides which backend a run reads, keeps the two mutually exclusive, and
// is the only place that knows both clients exist. Everything downstream —
// BuildPrompt's inject slot, the run row's memory PartReport, the MCP overlay —
// keeps its existing shape, so a backend swap is a configuration change rather
// than a second code path through the run loop.
//
// Failure contract, inherited from both clients: memory never blocks a task.
// An unreachable backend, an unreadable scope, a malformed answer or a missing
// scope id all degrade to a run WITHOUT memory plus a run-row note that says
// which of those happened (M-140).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/initctx"
	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/powercontext"
)

// memoryBackend identifies which memory plane this host reads.
type memoryBackend string

const (
	memoryBackendNone         memoryBackend = ""
	memoryBackendMoct         memoryBackend = "moct"
	memoryBackendPowerContext memoryBackend = "powercontext"
)

// powerContextMCPServerName is the name the PowerContext MCP entry gets in a
// task's MCP config. A bare TOML key, same constraint as `moct` and `multica`.
const powerContextMCPServerName = "powercontext"

// memoryBackendFor picks the backend for this host.
//
// The switch only ever subtracts: with neither URL set the claim path is
// byte-identical to what it was before the seam existed. With both set MOCT
// wins — it is the older, live-verified seam, and a host that has one is not
// silently migrated by exporting a second variable. The caller reports the
// conflict on the run row (memoryConflictNote) rather than logging it once at
// startup, because the claim path is the only place a reader can see which
// backend actually produced their run's block.
func memoryBackendFor(cfg Config) (kind memoryBackend, baseURL, token string) {
	moctURL := strings.TrimSpace(cfg.MoctBaseURL)
	pcURL := strings.TrimSpace(cfg.PowerContextBaseURL)
	switch {
	case moctURL != "":
		return memoryBackendMoct, moctURL, ""
	case pcURL != "":
		return memoryBackendPowerContext, pcURL, strings.TrimSpace(cfg.PowerContextToken)
	default:
		return memoryBackendNone, "", ""
	}
}

// memoryConflict reports whether a host has both backends configured, which
// means PowerContext is inert there. Callers surface it as a warning so a lane
// that "turned on PowerContext" and still sees moct blocks can tell why.
func memoryConflict(cfg Config) bool {
	return strings.TrimSpace(cfg.MoctBaseURL) != "" && strings.TrimSpace(cfg.PowerContextBaseURL) != ""
}

// memoryInjectPrompt fetches the pre-run block from whichever backend this
// host runs, and returns the memory row for the run.
//
// The memory part is returned on EVERY path, including the empty ones: "no
// memory because the workspace switched it off", "no memory because the
// backend was down" and "no memory because there is nothing to say" are
// different facts, and the run row has to tell them apart.
func memoryInjectPrompt(ctx context.Context, cfg Config, task Task, logger *slog.Logger) (string, initctx.PartReport) {
	kind, baseURL, token := memoryBackendFor(cfg)
	started := time.Now()
	block, part := memoryBackendPrompt(ctx, kind, baseURL, token, cfg, task, logger)
	// The injected block and its price belong beside it (M-616): the compare
	// panel pits this read against the shadow's, and a latency, a size or a
	// block only one side reports is not a comparison. Latency is measured
	// around the backend call itself, so client construction and decoding are
	// included and nothing else is; block and bytes are what buildPrompt will
	// prepend verbatim, recorded here rather than re-rendered later.
	part.Backend = string(kind)
	part.Block = block
	part.Bytes = len(block)
	part.LatencyMS = time.Since(started).Milliseconds()
	return block, part
}

// memoryBackendPrompt is the per-backend half of memoryInjectPrompt, kept
// separate so the caller can time and label whichever backend ran.
func memoryBackendPrompt(ctx context.Context, kind memoryBackend, baseURL, token string, cfg Config, task Task, logger *slog.Logger) (string, initctx.PartReport) {
	switch kind {
	case memoryBackendMoct:
		block, part := moctInjectPrompt(ctx, baseURL, task, logger)
		if memoryConflict(cfg) && logger != nil {
			logger.Warn("memory: both MOCT_BASE_URL and POWERCONTEXT_BASE_URL are set; using moct-be",
				"task_id", task.ID)
		}
		return block, part
	case memoryBackendPowerContext:
		return powerContextInjectPrompt(ctx, baseURL, token, task, logger)
	default:
		// No backend on this host at all. The note is backend-neutral on
		// purpose: naming one of the two would suggest the other is running.
		return "", initctx.PartReport{
			Part:     initctx.PartMemory,
			Measured: true,
			Note:     "no memory backend is configured on this daemon (neither MOCT_BASE_URL nor POWERCONTEXT_BASE_URL); no memory was injected",
		}
	}
}

// powerContextInjectPrompt is the PowerContext half of the pre-run push.
//
// The byte budget comes from the same place moct's token budget does, converted
// with the report's own estimator ((bytes+3)/4): one number prices the memory
// part on both backends, so two runs are comparable and a backend swap does not
// silently change how much context the agent gets.
func powerContextInjectPrompt(ctx context.Context, baseURL, token string, task Task, logger *slog.Logger) (string, initctx.PartReport) {
	off := func(note string) (string, initctx.PartReport) {
		return "", initctx.PartReport{Part: initctx.PartMemory, Measured: true, Note: note}
	}
	if strings.TrimSpace(baseURL) == "" {
		return off("PowerContext seam is off on this daemon (no base URL); no memory was injected")
	}
	// The switch only ever subtracts, exactly as on the moct path: nil means a
	// server that predates it, and the run then gets memory.
	if task.MoctMemoryInject != nil && !*task.MoctMemoryInject {
		return off("the workspace switched the pre-run memory block off (S2-C, M-123)")
	}
	scope, ok := powercontext.ScopeForTask(task.ProjectID, task.WorkspaceID)
	if !ok {
		return off("task carries no project or workspace identity, so there is no memory scope to read")
	}
	query := powerContextQuery(task)
	if query == "" {
		// PowerContext 1.6.0 rejects a prepare with no query (HTTP 422). An
		// assignment run carries no trigger comment, so it would otherwise be
		// scored as an empty backend on most runs (M-625).
		return off("this run carries no text to recall; the PowerContext prepare was skipped")
	}
	client, err := powercontext.NewClient(powercontext.Config{BaseURL: baseURL, Token: token})
	if err != nil {
		if logger != nil {
			logger.Warn("powercontext: base URL invalid; running without memory",
				"base_url", baseURL, "error", err)
		}
		return off("this daemon's PowerContext base URL does not parse; the run went without memory")
	}
	prepared, err := client.Prepare(ctx, powercontext.PrepareRequest{
		ScopeID:  scope,
		Query:    query,
		MaxBytes: powerContextMaxBytes(),
	})
	if err != nil {
		if logger != nil {
			logger.Warn("powercontext: pre-run prepare failed; running without memory",
				"task_id", task.ID, "scope", scope, "error", err)
		}
		return off("PowerContext pre-run prepare failed (" + powerContextErrorNote(err) + "); the run went without memory")
	}
	if prepared.IsEmpty() {
		return off("powercontext returned no context for scope " + scope)
	}
	content := prepared.Text()
	block := renderPowerContextBlock(content)
	part := initctx.PartReport{
		Part:     initctx.PartMemory,
		Measured: true,
		Tokens:   initctx.Estimate(block),
		Note: fmt.Sprintf("prepared %d bytes for scope %s (budget %d bytes)",
			prepared.ContentBytes, scope, powerContextMaxBytes()),
	}
	return block, part
}

// powerContextMaxBytes is the prepare byte budget derived from the memory
// part's token budget. The inverse of initctx's (bytes+3)/4 estimator, so the
// two backends price the same slice of the first request.
func powerContextMaxBytes() int {
	return powercontext.ClampMaxBytes(initctx.BudgetMemory * 4)
}

// powerContextQuery is the recall query for a run: the best text the claim
// carries about what the run is about.
//
// Only about 15% of sampled tasks carried a trigger comment, and PowerContext
// 1.6.0 rejects a prepare without `query` (HTTP 422). The fallback order is the
// same order a reader would use to understand the run: the human's latest
// instruction, the issue the task belongs to, the autopilot or chat that
// spawned it, then a handoff. Nothing here enters the prompt — it is a bounded
// query, not context — and an empty result skips the prepare instead of asking
// the backend to guess (M-625).
func powerContextQuery(task Task) string {
	candidates := []string{
		task.TriggerCommentContent,
		strings.TrimSpace(task.IssueTitle + " " + task.IssueDescription),
		strings.TrimSpace(task.AutopilotTitle + " " + task.AutopilotDescription),
		powerContextChatQuery(task),
		task.QuickCreatePrompt,
		task.HandoffNote,
	}
	for _, candidate := range candidates {
		if query := moctOneLine(candidate, powerContextQueryMaxRunes); query != "" {
			return query
		}
	}
	return ""
}

// powerContextChatQuery prefers the member-authored message over the generic
// chat field: the latter can be a kickoff or a summary written by the system.
func powerContextChatQuery(task Task) string {
	for _, text := range []*string{task.ChatSkillMessage, task.ChatResumeMessage} {
		if text != nil && strings.TrimSpace(*text) != "" {
			return *text
		}
	}
	return task.ChatMessage
}

// powerContextQueryMaxRunes bounds the recall query. Retrieval quality falls
// off long before a whole comment is useful, and Topic search caps its own
// query at 64 distinct terms.
const powerContextQueryMaxRunes = 240

// powerContextErrorNote turns a client error into the short parenthetical a
// run row can carry. A configuration fact (an unreadable scope) is worth
// telling apart from an outage, and the client already separates them.
func powerContextErrorNote(err error) string {
	var apiErr *powercontext.APIError
	if ok := asAPIError(err, &apiErr); ok {
		if apiErr.Code != "" {
			return fmt.Sprintf("HTTP %d %s", apiErr.StatusCode, apiErr.Code)
		}
		return fmt.Sprintf("HTTP %d", apiErr.StatusCode)
	}
	return "backend unreachable"
}

// asAPIError is errors.As without pulling errors into this file's import list
// for one call; kept tiny and obvious rather than generic.
func asAPIError(err error, target **powercontext.APIError) bool {
	for err != nil {
		if apiErr, ok := err.(*powercontext.APIError); ok {
			*target = apiErr
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// renderPowerContextBlock wraps the prepared text in one heading so the agent
// can tell remembered context from its instructions. The text itself is
// returned verbatim: PowerContext already applied the byte budget and the
// citations, and re-trimming it here would break the receipts it embedded.
func renderPowerContextBlock(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	return "## Project memory (PowerContext)\n\n" + trimmed + "\n\n"
}

// powerContextMCPConfig builds the MCP overlay entry that gives a run the
// PowerContext tools, or ok=false when the entry must stay out of this run's
// config.
//
// Unlike moct, PowerContext ships its own MCP server at /mcp, so there is no
// stdio wrapper to install and no second client artifact: the entry is the
// remote HTTP endpoint itself, and the run's scope is bound by the server's own
// Access rules rather than by env-encoded refs.
func powerContextMCPConfig(baseURL, token string, task Task) (json.RawMessage, bool) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, false
	}
	// A task with no identity has no scope to read; adding the tools would
	// only let an agent make calls the server answers 403 to.
	if _, ok := powercontext.ScopeForTask(task.ProjectID, task.WorkspaceID); !ok {
		return nil, false
	}
	if _, err := powercontext.NewClient(powercontext.Config{BaseURL: baseURL}); err != nil {
		return nil, false
	}
	entry := map[string]any{
		"type": "http",
		"url":  strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/mcp",
	}
	if tok := strings.TrimSpace(token); tok != "" {
		entry["headers"] = map[string]string{"Authorization": "Bearer " + tok}
	}
	raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		powerContextMCPServerName: entry,
	}})
	if err != nil {
		return nil, false
	}
	return raw, true
}

// powerContextHandoffCommentMax bounds the run comment copied into a handoff
// entry. PowerContext caps an atomic memory body at 8192 bytes; a handoff also
// carries refs and a branch name, and an agent's closing comment can be long
// enough to fill that alone.
const powerContextHandoffCommentMax = 1800

// powerContextRecordHandoff is step 13 of the run loop: at run end, commit the
// outcome as a durable memory entry so the NEXT run's pre-run prepare recalls
// it instead of starting cold.
//
// Idempotency: a run end can be reported more than once (a retried terminal
// callback, a daemon restart re-reading its own state), and a second copy of
// the same handoff would inject twice next time. Each entry therefore carries a
// stable marker — the task id — and the write is skipped when a search for that
// marker already returns a record.
//
// Never fatal, like every other path in the seam: a backend that is down, a
// scope that does not exist or a rejected write logs a warning and the run's
// own result is unaffected.
func powerContextRecordHandoff(ctx context.Context, cfg Config, task Task, result TaskResult, logger *slog.Logger) {
	kind, baseURL, token := memoryBackendFor(cfg)
	if kind != memoryBackendPowerContext {
		return
	}
	scope, ok := powercontext.ScopeForTask(task.ProjectID, task.WorkspaceID)
	if !ok {
		return
	}
	// A task that never started (no session, no branch, no comment) has no
	// outcome worth remembering.
	if result.SessionID == "" && result.BranchName == "" && strings.TrimSpace(result.Comment) == "" {
		return
	}
	client, err := powercontext.NewClient(powercontext.Config{BaseURL: baseURL, Token: token})
	if err != nil {
		if logger != nil {
			logger.Warn("powercontext: handoff skipped; bad base URL", "base_url", baseURL, "error", err)
		}
		return
	}
	marker := powerContextHandoffMarker(task)
	if existing, searchErr := client.Search(ctx, powercontext.SearchParams{
		ScopeID: scope,
		Query:   marker,
		Limit:   3,
	}); searchErr == nil && existing != nil {
		for _, text := range existing.Texts() {
			if strings.Contains(text, marker) {
				return
			}
		}
	}
	text := powerContextHandoffText(task, result, marker)
	if _, err := client.Remember(ctx, powercontext.RememberRequest{
		ScopeID: scope,
		Kind:    "decision",
		Text:    text,
	}); err != nil {
		if logger != nil {
			logger.Warn("powercontext: handoff write failed",
				"task_id", task.ID, "scope", scope, "error", err)
		}
	}
}

// powerContextHandoffMarker is the idempotency key written into every handoff
// entry. It is also a valid search query: it names one task, so the dedupe read
// cannot collide with another run's entry.
func powerContextHandoffMarker(task Task) string {
	return "handoff:/multica/task/" + task.ID
}

// powerContextHandoffText is one handoff entry: what ran, what it left behind,
// and which refs it belongs to. Plain text, because it is recalled as context
// text rather than parsed.
func powerContextHandoffText(task Task, result TaskResult, marker string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", marker)
	if task.IssueID != "" {
		fmt.Fprintf(&b, "issue: %s\n", moct.IssueRef(task.IssueID))
	}
	if task.ProjectID != "" {
		fmt.Fprintf(&b, "project: %s\n", moct.ProjectRef(task.ProjectID))
	}
	if task.AgentID != "" {
		fmt.Fprintf(&b, "agent: %s\n", moct.ExpertRef(task.AgentID))
	}
	if result.SessionID != "" {
		fmt.Fprintf(&b, "session: %s\n", result.SessionID)
	}
	if status := strings.TrimSpace(result.Status); status != "" {
		fmt.Fprintf(&b, "outcome: %s\n", status)
	}
	if branch := strings.TrimSpace(result.BranchName); branch != "" {
		fmt.Fprintf(&b, "branch: %s\n", branch)
	}
	if comment := strings.TrimSpace(result.Comment); comment != "" {
		b.WriteString("report:\n")
		b.WriteString(powerContextClamp(comment, powerContextHandoffCommentMax))
		b.WriteString("\n")
	}
	return b.String()
}

// powerContextClamp bounds a copied string by bytes without splitting a rune.
func powerContextClamp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// utf8Start reports whether b begins a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
