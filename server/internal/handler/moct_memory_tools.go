package handler

import (
	"encoding/json"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// memoryToolNames are the three on-demand MOCT memory tools the daemon adds to
// a run's MCP config (server `moct`, docs/moct-seam.md). The transcript stores
// whatever the harness called them, so matching is by name, not by server.
var memoryToolNames = map[string]bool{
	"memory_search": true,
	"memory_get":    true,
	"memory_note":   true,
}

// memoryResultBudget caps one tool result in this response. The daemon already
// stores at most an 8 KiB preview (toolOutputPreviewBudget), so this only
// guards a row written by an older or different producer.
const memoryResultBudget = 8192

// memoryToolCall is one MOCT memory tool call as the stored transcript proves
// it: the tool_use row (name, arguments) and the tool_result row with the same
// call id, when one was recorded. Nothing here is inferred from the model's
// text, so "which records reached the model" is deliberately not answered
// beyond these calls and the initial inject.
type memoryToolCall struct {
	Seq       int32           `json:"seq"`
	CallID    string          `json:"call_id,omitempty"`
	Tool      string          `json:"tool"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// HasResult separates "the run recorded an empty result" from "no result
	// row exists" (the call was still running when the run ended).
	HasResult       bool   `json:"has_result"`
	Result          string `json:"result,omitempty"`
	ResultTruncated bool   `json:"result_truncated,omitempty"`
	// Error is a run error the transcript recorded while this call was the
	// latest one. The table has no per-tool isError column, so this is the only
	// error state the rows can prove; a failed MCP call that the harness folded
	// into its result text is not distinguishable and is not claimed.
	Error string `json:"error,omitempty"`
}

// memoryToolsReport is the "memory tools used in this run" half of the report.
// Available is false when the transcript could not be read at all: the UI must
// say "unavailable" rather than "no calls".
type memoryToolsReport struct {
	Available bool             `json:"available"`
	Calls     []memoryToolCall `json:"calls"`
}

// memoryToolCalls walks a task's transcript rows in seq order and returns its
// MOCT memory tool calls, each paired with its result. Rows of other types are
// ignored, so this never claims a call the run did not make.
func memoryToolCalls(rows []db.TaskMessage) []memoryToolCall {
	results := make(map[string]db.TaskMessage, len(rows))
	for _, row := range rows {
		if row.Type != "tool_result" || !row.CallID.Valid || row.CallID.String == "" {
			continue
		}
		if _, seen := results[row.CallID.String]; !seen {
			results[row.CallID.String] = row
		}
	}

	calls := make([]memoryToolCall, 0, 4)
	for index, row := range rows {
		if row.Type != "tool_use" {
			continue
		}
		name, ok := memoryToolName(row.Tool.String)
		if !ok {
			continue
		}
		call := memoryToolCall{
			Seq:  row.Seq,
			Tool: row.Tool.String,
			Name: name,
		}
		if row.CallID.Valid {
			call.CallID = row.CallID.String
		}
		if len(row.Input) > 0 {
			call.Arguments = json.RawMessage(row.Input)
		}
		if row.CallID.Valid {
			if result, found := results[row.CallID.String]; found {
				call.HasResult = true
				producerTruncated := result.OutputTruncated.Valid && result.OutputTruncated.Bool
				call.Result, call.ResultTruncated = memoryResultText(result.Output.String, producerTruncated)
			}
		}
		// The transcript's error rows carry no call id, so they are only
		// attributable to the call they follow.
		for _, later := range rows[index+1:] {
			if later.Type == "tool_use" {
				break
			}
			if later.Type == "error" && later.Content.String != "" {
				call.Error, _ = memoryResultText(later.Content.String, false)
				break
			}
		}
		calls = append(calls, call)
	}
	return calls
}

// memoryResultText bounds one stored text for the response and reports whether
// anything was dropped, including the producer's own truncation flag.
func memoryResultText(text string, producerTruncated bool) (string, bool) {
	if len(text) <= memoryResultBudget {
		return text, producerTruncated
	}
	end := memoryResultBudget
	for end > 0 && !utf8Start(text[end]) {
		end--
	}
	return text[:end], true
}

// utf8Start reports whether b begins a rune, so a cut never splits one.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// memoryToolName returns the MOCT memory tool a transcript tool name refers to.
// Harnesses record MCP tools as `mcp__<server>__<tool>` (server/internal/
// integrations/wecom/progress_render.go has the same split) and the tool's bare
// name otherwise. Only the moct server is ours: another MCP server's
// `memory_search` is a different tool and must not be reported as MOCT memory.
func memoryToolName(tool string) (string, bool) {
	name := strings.TrimSpace(tool)
	if memoryToolNames[name] {
		return name, true
	}
	qualified := strings.TrimPrefix(name, "mcp__")
	server, rest, ok := strings.Cut(qualified, "__")
	if !ok || !strings.Contains(strings.ToLower(server), "moct") {
		return "", false
	}
	if memoryToolNames[rest] {
		return rest, true
	}
	return "", false
}
