package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// S1-A1 (M-118): edge validation enforces the team-room limits so a malformed
// ask/plan never reaches the database.

func TestValidateAskRequest(t *testing.T) {
	okQuestions := func(n int) []askQuestionRequest {
		qs := make([]askQuestionRequest, 0, n)
		for range n {
			qs = append(qs, askQuestionRequest{
				Prompt:  "q",
				Options: []askOptionRequest{{Label: "a"}, {Label: "b"}},
			})
		}
		return qs
	}
	for _, tc := range []struct {
		name    string
		req     askRequest
		wantErr bool
	}{
		{"one question, two options", askRequest{Questions: okQuestions(1)}, false},
		{"six questions (the max)", askRequest{Questions: okQuestions(6)}, false},
		{"no questions", askRequest{}, true},
		{"seven questions", askRequest{Questions: okQuestions(7)}, true},
		{"empty prompt", askRequest{Questions: []askQuestionRequest{{Prompt: "", Options: []askOptionRequest{{Label: "a"}, {Label: "b"}}}}}, true},
		{"prompt over the limit", askRequest{Questions: []askQuestionRequest{{Prompt: strings.Repeat("x", 301), Options: []askOptionRequest{{Label: "a"}, {Label: "b"}}}}}, true},
		{"only one option", askRequest{Questions: []askQuestionRequest{{Prompt: "q", Options: []askOptionRequest{{Label: "a"}}}}}, true},
		{"five options (over the max)", askRequest{Questions: []askQuestionRequest{{Prompt: "q", Options: []askOptionRequest{{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}}}}}, true},
		{"context over the limit", askRequest{Context: strings.Repeat("x", 1201), Questions: okQuestions(1)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAskRequest(tc.req)
			if tc.wantErr && err == nil {
				t.Error("validateAskRequest accepted an invalid request")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateAskRequest rejected a valid request: %v", err)
			}
		})
	}
}

func TestValidateCardAnswers(t *testing.T) {
	questionsJSON := []byte(`[
		{"prompt":"Pick one","options":[{"label":"A"},{"label":"B"}]},
		{"prompt":"Pick many","multiple":true,"options":[{"label":"X"},{"label":"Y"},{"label":"Z"}]}
	]`)
	one := func(sel ...int) []planAnswerItem { return []planAnswerItem{{Selected: sel}} }
	for _, tc := range []struct {
		name    string
		answers []planAnswerItem
		wantErr bool
	}{
		{"valid: one each in order", []planAnswerItem{{Selected: []int{0}}, {Selected: []int{0, 2}, Text: "and lint"}}, false},
		{"valid: free text with no selection", []planAnswerItem{{Text: "my own words"}, {Selected: []int{1}}}, false},
		{"wrong count", one(0), true},
		{"too many answers", append(one(0), planAnswerItem{Selected: []int{0}}, planAnswerItem{Selected: []int{0}}), true},
		{"no selection and no text", []planAnswerItem{{}, {Selected: []int{0}}}, true},
		{"multiple picks on a single-choice question", []planAnswerItem{{Selected: []int{0, 1}}, {Selected: []int{0}}}, true},
		{"out-of-range index", []planAnswerItem{{Selected: []int{5}}, {Selected: []int{0}}}, true},
		{"negative index", []planAnswerItem{{Selected: []int{-1}}, {Selected: []int{0}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCardAnswers(questionsJSON, tc.answers)
			if tc.wantErr && err == nil {
				t.Error("validateCardAnswers accepted invalid answers")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateCardAnswers rejected valid answers: %v", err)
			}
		})
	}
}

func TestMergePlanSection(t *testing.T) {
	base := []byte(`[{"sectionId":"s1","heading":"Goal","markdown":"old"}]`)

	// Replace an existing section in place.
	merged, err := mergePlanSection(base, "s1", "Goal", "new")
	if err != nil {
		t.Fatal(err)
	}
	var sections []map[string]any
	if err := json.Unmarshal(merged, &sections); err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || sections[0]["markdown"] != "new" {
		t.Fatalf("replace produced %s", merged)
	}

	// Append a new section when the id is unknown.
	merged, err = mergePlanSection(base, "s2", "Steps", "do it")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(merged, &sections); err != nil {
		t.Fatal(err)
	}
	if len(sections) != 2 || sections[1]["sectionId"] != "s2" {
		t.Fatalf("append produced %s", merged)
	}
}

func TestIsPlanningStatus(t *testing.T) {
	for _, s := range []string{"backlog", "todo", "planing_2"} {
		if !isPlanningStatus(s) {
			t.Errorf("isPlanningStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"in_progress", "in_review", "done", "cancelled", ""} {
		if isPlanningStatus(s) {
			t.Errorf("isPlanningStatus(%q) = true, want false", s)
		}
	}
}

func TestIsFinalPlanStatusHandler(t *testing.T) {
	// The handler keeps its own copy to avoid exporting a service symbol; it
	// must agree with the service's notion of finality.
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
