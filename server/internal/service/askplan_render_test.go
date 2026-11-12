package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// S1-A1 (M-118): the renderers are the only channel the resumed agent sees
// the human's reply through, so their output is the model-visible contract.

func TestRenderPlanBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sections string
		want     string
	}{
		{
			name:     "two sections joined by a blank line",
			sections: `[{"sectionId":"s1","heading":"Goal","markdown":"Ship it"},{"sectionId":"s2","heading":"Steps","markdown":"Do the thing"}]`,
			want:     "## Goal\n\nShip it\n\n## Steps\n\nDo the thing",
		},
		{
			name:     "empty sections skipped",
			sections: `[{"sectionId":"s1","heading":"Goal","markdown":"Ship it"},{"sectionId":"s2","heading":"","markdown":""}]`,
			want:     "## Goal\n\nShip it",
		},
		{
			name:     "body without a heading is kept bare",
			sections: `[{"sectionId":"s1","heading":"","markdown":"just body"}]`,
			want:     "just body",
		},
		{
			name:     "completely empty plan renders empty (never promoted)",
			sections: `[{"sectionId":"s1","heading":"","markdown":""}]`,
			want:     "",
		},
		{
			name:     "empty array renders empty",
			sections: `[]`,
			want:     "",
		},
		{
			name:     "unreadable JSON renders empty",
			sections: `not-json`,
			want:     "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderPlanBody([]byte(tc.sections)); got != tc.want {
				t.Errorf("renderPlanBody = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderQuestionAnswerHandoff(t *testing.T) {
	cardJSON, _ := json.Marshal([]map[string]any{
		{"prompt": "Which release day?", "options": []map[string]string{{"label": "Thursday"}, {"label": "Friday"}}},
		{"prompt": "Which checks?", "options": []map[string]string{{"label": "Unit"}, {"label": "Browser"}}},
	})
	card := questionCardForTest(cardJSON)
	answersJSON, _ := json.Marshal([]map[string]any{
		{"selected": []int{0}},
		{"selected": []int{0, 1}, "text": "and lint"},
	})
	got := renderQuestionAnswerHandoff(card, answersJSON)
	for _, want := range []string{
		"Your question was answered by a human",
		"Q1: Which release day?",
		"Thursday", // selected label, not the unselected Friday
		"Q2: Which checks?",
		"Unit",                  // first selected label
		"Browser",               // second selected label
		"Answer text: and lint", // the free-text answer
	} {
		if !strings.Contains(got, want) {
			t.Errorf("handoff missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Friday") {
		t.Errorf("handoff leaked an unselected option:\n%s", got)
	}
}

func TestRenderQuestionAnswerHandoffWithContext(t *testing.T) {
	cardJSON, _ := json.Marshal([]map[string]any{
		{"prompt": "Pick one", "options": []map[string]string{{"label": "A"}, {"label": "B"}}},
	})
	card := questionCardForTest(cardJSON)
	card.Context = pgtype.Text{String: "We are choosing a lane.", Valid: true}
	answersJSON, _ := json.Marshal([]map[string]any{{"selected": []int{1}}})
	got := renderQuestionAnswerHandoff(card, answersJSON)
	if !strings.Contains(got, "We are choosing a lane.") {
		t.Errorf("handoff missing the ask context:\n%s", got)
	}
	if !strings.Contains(got, "\n  - B") {
		t.Errorf("handoff missing the selected label B:\n%s", got)
	}
}

func TestRenderPlanVerdictHandoff(t *testing.T) {
	plan := planForTest()
	plan.SubmittedRevision = pgtype.Int4{Int32: 3, Valid: true}

	approve := renderPlanVerdictHandoff(plan, "approve", nil, "")
	for _, want := range []string{`"Release plan"`, "revision 3", "APPROVED", "written into the issue description"} {
		if !strings.Contains(approve, want) {
			t.Errorf("approve handoff missing %q:\n%s", want, approve)
		}
	}

	commentsJSON, _ := json.Marshal([]string{"Tighten the rollback step", "Add a metrics section"})
	changes := renderPlanVerdictHandoff(plan, "changes_requested", commentsJSON, "")
	for _, want := range []string{"CHANGES REQUESTED", "Tighten the rollback step", "Add a metrics section", "submit it again"} {
		if !strings.Contains(changes, want) {
			t.Errorf("changes handoff missing %q:\n%s", want, changes)
		}
	}

	cancel := renderPlanVerdictHandoff(plan, "cancel", nil, "")
	if !strings.Contains(cancel, "CANCELLED") || !strings.Contains(cancel, "Do not act on it") {
		t.Errorf("cancel handoff wrong:\n%s", cancel)
	}

	withNote := renderPlanVerdictHandoff(plan, "approve", nil, "Ship on Thursday.")
	if !strings.Contains(withNote, "Note: Ship on Thursday.") {
		t.Errorf("verdict handoff missing the note:\n%s", withNote)
	}
}

func TestHasReviewComment(t *testing.T) {
	empty, _ := json.Marshal([]string{})
	spaced, _ := json.Marshal([]string{"   "})
	filled, _ := json.Marshal([]string{"make it shorter"})
	for _, tc := range []struct {
		name     string
		comments json.RawMessage
		note     string
		want     bool
	}{
		{"nothing", empty, "", false},
		{"blank note only", empty, "   ", false},
		{"one real comment", filled, "", true},
		{"note only", empty, "do it", true},
		{"whitespace comment only", spaced, "", false},
		{"unreadable comments and no note", json.RawMessage("not-json"), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasReviewComment(tc.comments, tc.note); got != tc.want {
				t.Errorf("hasReviewComment = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsFinalPlanStatus(t *testing.T) {
	for _, s := range []string{"approved", "cancelled"} {
		if !isFinalPlanStatus(s) {
			t.Errorf("isFinalPlanStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"draft", "in_review", "changes_requested", ""} {
		if isFinalPlanStatus(s) {
			t.Errorf("isFinalPlanStatus(%q) = true, want false", s)
		}
	}
}

// questionCardForTest builds a card row with the given questions JSON.
func questionCardForTest(questions json.RawMessage) db.AgentQuestionCard {
	return db.AgentQuestionCard{Questions: questions}
}

// planForTest builds a plan row with a title and sections.
func planForTest() db.AgentPlan {
	return db.AgentPlan{Title: "Release plan", Sections: json.RawMessage(`[]`)}
}
