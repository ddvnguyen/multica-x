// Event model for dsh session-v4 JSONL logs, and the translation into the
// Multica task-message shape the web transcript renders.
//
// Only the handful of event types the transcript needs are modeled; every
// other v4 event type (turn bookkeeping, sandbox/approval policy, compaction,
// subagent plumbing, …) is carried as raw JSON and skipped by Translate.
package dshsession

import "encoding/json"

// Event is one decoded JSONL line. Data is kept raw: the shapes that matter
// (assistant/message, tool/call, tool/result) are unpacked by Translate.
type Event struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Time int64           `json:"time"` // epoch milliseconds
	Data json.RawMessage `json:"data"`
}

// contentPart is one element of a dsh message content array. Observed part
// types on a live store: "text", "reasoning", "tool-call".
type contentPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"` // tool-call id on "tool-call" parts
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// messageData is the shape of assistant/message and system/message payloads:
// { "turn": N, "step": N, "message": { "role": ..., "content": [...] } }.
type messageData struct {
	Message struct {
		Role    string        `json:"role"`
		Content []contentPart `json:"content"`
	} `json:"message"`
}

// toolCallData is the shape of tool/call payloads.
type toolCallData struct {
	CallID    string          `json:"callId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolResultData is the shape of tool/result payloads:
// { "message": { "role": "tool", "toolCallId": ..., "isError": ..., "content": [...] } }.
type toolResultData struct {
	Message struct {
		ToolCallID string        `json:"toolCallId"`
		IsError    bool          `json:"isError"`
		Content    []contentPart `json:"content"`
	} `json:"message"`
}

// Entry is one translated transcript row in the task-message shape the web
// transcript renders. CreatedAt is epoch milliseconds; Seq is the row's
// position in the translated stream (1-based, dense) — dsh's own seq numbers
// are sparse across event kinds, and the transcript endpoint's since-paging
// needs a dense stream.
type Entry struct {
	Seq       int
	Type      string // "text", "thinking", "tool_use", "tool_result"
	Tool      string
	CallID    string
	Content   string
	Input     string // raw JSON of tool arguments, "" when none
	Output    string
	IsError   bool
	CreatedAt int64
}
