package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The answer is what the model reads back as the tool result, so a hand-typed
// label that differs only in case is stored under the label the question
// offered. Everything else is free text: the provider gives every question an
// implicit "Other" field, so a non-label answer is legitimate and must survive.
func TestCanonicalQuestionAnswer(t *testing.T) {
	options, err := json.Marshal([]QuestionOption{
		{Label: "Postgres"},
		{Label: "SQLite"},
	})
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}

	cases := []struct {
		name    string
		options []byte
		multi   bool
		answer  string
		want    string
	}{
		{name: "exact label", options: options, answer: "Postgres", want: "Postgres"},
		{name: "label case differs", options: options, answer: "postgres", want: "Postgres"},
		{name: "other free text", options: options, answer: "a different database", want: "a different database"},
		{name: "no options keeps answer", answer: "anything", want: "anything"},
		{name: "unparsable options keeps answer", options: []byte("nope"), answer: "postgres", want: "postgres"},
		{
			name:    "multi-select joined labels",
			options: options,
			multi:   true,
			answer:  "Postgres, SQLite",
			want:    "Postgres, SQLite",
		},
		{
			name:    "multi-select labels are canonicalised and respaced",
			options: options,
			multi:   true,
			answer:  "sqlite,POSTGRES",
			want:    "SQLite, Postgres",
		},
		{
			name:    "multi-select free text is kept",
			options: options,
			multi:   true,
			answer:  "Postgres, something else",
			want:    "Postgres, something else",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalQuestionAnswer(db.Question{Options: tc.options, MultiSelect: tc.multi}, tc.answer)
			if got != tc.want {
				t.Fatalf("canonicalQuestionAnswer(%q) = %q, want %q", tc.answer, got, tc.want)
			}
		})
	}
}

// An empty label renders as an unclickable button on both surfaces, so it is
// dropped at the door rather than stored and filtered later.
func TestNormalizeQuestionOptionsDropsBlankLabels(t *testing.T) {
	got := normalizeQuestionOptions([]QuestionOption{
		{Label: "  Postgres  ", Description: " server "},
		{Label: "   "},
		{Label: "SQLite"},
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 options, got %d: %+v", len(got), got)
	}
	if got[0].Label != "Postgres" || got[0].Description != "server" {
		t.Fatalf("first option was not trimmed: %+v", got[0])
	}
	if got[1].Label != "SQLite" {
		t.Fatalf("second option = %+v", got[1])
	}
}
