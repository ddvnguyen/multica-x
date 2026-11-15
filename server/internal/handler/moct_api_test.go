package handler

import (
	"encoding/json"
	"testing"
)

func TestMemoryReportPart(t *testing.T) {
	tests := []struct {
		name, result string
		wantNote     string
		wantNull     bool
	}{
		{name: "memory reason is returned", result: `{"init_context":{"parts":[{"part":"instructions"},{"part":"memory","tokens":0,"note":"MOCT seam is off"}]}}`, wantNote: "MOCT seam is off"},
		{name: "missing report", result: `{}`, wantNull: true},
		{name: "malformed report", result: `not json`, wantNull: true},
		{name: "report without memory", result: `{"init_context":{"parts":[{"part":"task"}]}}`, wantNull: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryReportPart(json.RawMessage(tc.result))
			if tc.wantNull {
				if string(got) != "null" {
					t.Fatalf("memory = %s, want null", got)
				}
				return
			}
			var part map[string]any
			if err := json.Unmarshal(got, &part); err != nil {
				t.Fatalf("decode memory part: %v", err)
			}
			if part["note"] != tc.wantNote {
				t.Fatalf("note = %v, want %q", part["note"], tc.wantNote)
			}
		})
	}
}
