package main

// M-500: `question-card show` must print the chosen answers and their
// provenance for an answered card, and keep showing questions and options for
// an open one. These pin the decode contract the show command reads.

import (
	"encoding/json"
	"strings"
	"testing"
)

func answeredCardJSON() string {
	return `{
		"id": "card-1",
		"status": "answered",
		"context": "We are choosing a release lane.",
		"answered_by": "user-1",
		"answered_by_type": "member",
		"answered_at": "2026-11-22T03:00:00Z",
		"questions": [
			{"prompt": "Which release day?", "options": [{"label": "Thursday"}, {"label": "Friday"}]},
			{"prompt": "Anything else?", "options": [{"label": "Yes"}, {"label": "No"}]}
		],
		"answers": [
			{"selected": [0]},
			{"text": "ship it Friday"}
		]
	}`
}

func TestQuestionCardShowDecodesAnsweredCard(t *testing.T) {
	var card questionCardPayload
	if err := json.Unmarshal([]byte(answeredCardJSON()), &card); err != nil {
		t.Fatalf("decode answered card: %v", err)
	}
	if card.Status != "answered" {
		t.Fatalf("status = %q, want answered", card.Status)
	}
	if len(card.Answers) != 2 {
		t.Fatalf("answers = %#v, want two", card.Answers)
	}
	if len(card.Answers[0].Selected) != 1 || card.Answers[0].Selected[0] != 0 {
		t.Fatalf("first answer = %#v, want selected [0]", card.Answers[0])
	}
	if strings.TrimSpace(card.Answers[1].Text) != "ship it Friday" {
		t.Fatalf("second answer text = %q", card.Answers[1].Text)
	}
	if card.AnsweredByType != "member" || card.AnsweredBy == "" || card.AnsweredAt == "" {
		t.Fatalf("provenance = by %q type %q at %q, want the Owner and a timestamp",
			card.AnsweredBy, card.AnsweredByType, card.AnsweredAt)
	}
	// The first question's option index must resolve to Thursday.
	if card.Questions[0].Options[card.Answers[0].Selected[0]].Label != "Thursday" {
		t.Fatalf("selected index does not resolve to Thursday: %#v", card)
	}
}

func TestQuestionCardShowDecodesOpenCard(t *testing.T) {
	var card questionCardPayload
	if err := json.Unmarshal([]byte(`{
		"id": "card-2", "status": "open",
		"questions": [{"prompt": "Which release day?", "options": [{"label": "Thursday"}, {"label": "Friday"}]}]
	}`), &card); err != nil {
		t.Fatalf("decode open card: %v", err)
	}
	if card.Status != "open" || len(card.Questions) != 1 {
		t.Fatalf("open card = %#v", card)
	}
	if len(card.Answers) != 0 || card.AnsweredByType != "" {
		t.Fatalf("open card must carry no answers or provenance: %#v", card)
	}
}
