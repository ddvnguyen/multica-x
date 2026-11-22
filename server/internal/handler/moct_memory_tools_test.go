package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func text(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }

func toolUse(seq int32, tool, callID string, input string) db.TaskMessage {
	row := db.TaskMessage{Seq: seq, Type: "tool_use", Tool: text(tool), CallID: text(callID)}
	if input != "" {
		row.Input = []byte(input)
	}
	return row
}

func toolResult(seq int32, callID, output string, truncated bool) db.TaskMessage {
	return db.TaskMessage{
		Seq: seq, Type: "tool_result", CallID: text(callID), Output: text(output),
		OutputTruncated: pgtype.Bool{Bool: truncated, Valid: true},
	}
}

func TestMemoryToolCallsExtractsTheRunsOwnCalls(t *testing.T) {
	rows := []db.TaskMessage{
		{Seq: 1, Type: "text", Content: text("working on it")},
		toolUse(2, "memory_search", "call-a", `{"q":"tier resolver"}`),
		toolResult(3, "call-a", `{"records":[{"id":"r1"}]}`, false),
		toolUse(4, "Bash", "call-b", `{"command":"ls"}`),
		toolResult(5, "call-b", "total 0", false),
		toolUse(6, "memory_get", "call-c", `{"ids":["r1"]}`),
		toolResult(7, "call-c", `{"records":[{"id":"r1","title":"Tier resolver"}]}`, false),
		toolUse(8, "memory_note", "call-d", `{"type":"decision","title":"Keep it"}`),
		{Seq: 9, Type: "error", Content: text("moct is unreachable")},
		toolUse(10, "memory_search", "call-e", `{"q":"second"}`),
	}

	calls := memoryToolCalls(rows)
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4: %+v", len(calls), calls)
	}
	if calls[0].Name != "memory_search" || !calls[0].HasResult || calls[0].Result == "" {
		t.Fatalf("search = %+v, want a result", calls[0])
	}
	var args map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil || args["q"] != "tier resolver" {
		t.Fatalf("search arguments = %s (err %v)", calls[0].Arguments, err)
	}
	if calls[1].Name != "memory_get" || calls[1].Result == "" {
		t.Fatalf("get = %+v, want a result", calls[1])
	}
	if calls[2].Name != "memory_note" || calls[2].HasResult {
		t.Fatalf("note = %+v, want no result", calls[2])
	}
	if calls[2].Error != "moct is unreachable" {
		t.Fatalf("note error = %q, want the run error after the call", calls[2].Error)
	}
	if calls[3].Error != "" || calls[3].HasResult {
		t.Fatalf("second search = %+v, want neither a result nor an error", calls[3])
	}
}

func TestMemoryToolCallsIgnoresOtherTools(t *testing.T) {
	rows := []db.TaskMessage{
		toolUse(1, "mcp__github__memory_search", "call-a", `{"q":"x"}`),
		toolResult(2, "call-a", "ok", false),
		toolUse(3, "Read", "call-b", `{"path":"/tmp/x"}`),
		toolUse(4, "mcp__moct__memory_search", "call-c", `{"q":"y"}`),
		toolResult(5, "call-c", "found", false),
		toolUse(6, "mcp__moct__memory_get", "call-d", `{"ids":["r1"]}`),
		toolResult(7, "call-d", "record", false),
	}
	calls := memoryToolCalls(rows)
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want only the two moct calls: %+v", len(calls), calls)
	}
	if calls[0].Tool != "mcp__moct__memory_search" || calls[0].Name != "memory_search" {
		t.Fatalf("first call = %+v, want the qualified search", calls[0])
	}
	if calls[1].Name != "memory_get" || calls[1].Result != "record" {
		t.Fatalf("second call = %+v, want the get result", calls[1])
	}
}

func TestMemoryToolCallsEmptyTranscript(t *testing.T) {
	calls := memoryToolCalls(nil)
	if calls == nil || len(calls) != 0 {
		t.Fatalf("calls = %v, want an empty, non-nil slice", calls)
	}
}

func TestMemoryToolCallsMarksLongResults(t *testing.T) {
	long := strings.Repeat("a", memoryResultBudget+10)
	rows := []db.TaskMessage{
		toolUse(1, "memory_search", "call-a", ""),
		toolResult(2, "call-a", long, false),
	}
	calls := memoryToolCalls(rows)
	if len(calls) != 1 || !calls[0].ResultTruncated {
		t.Fatalf("calls = %+v, want one truncated result", calls)
	}
	if len(calls[0].Result) != memoryResultBudget {
		t.Fatalf("result length = %d, want %d", len(calls[0].Result), memoryResultBudget)
	}
	if calls[0].Arguments != nil {
		t.Fatalf("arguments = %s, want none for a row without input", calls[0].Arguments)
	}
}

func TestMemoryToolName(t *testing.T) {
	tests := []struct {
		tool, want string
		ok         bool
	}{
		{"memory_search", "memory_search", true},
		{"memory_get", "memory_get", true},
		{"memory_note", "memory_note", true},
		{"mcp__moct__memory_note", "memory_note", true},
		{"moct__memory_get", "memory_get", true},
		{"mcp__github__memory_search", "", false},
		{"Read", "", false},
		{"", "", false},
		{"memory_search_all", "", false},
	}
	for _, tc := range tests {
		got, ok := memoryToolName(tc.tool)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("memoryToolName(%q) = (%q, %v), want (%q, %v)", tc.tool, got, ok, tc.want, tc.ok)
		}
	}
}
