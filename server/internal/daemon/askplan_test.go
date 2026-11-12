package daemon

import (
	"net/http"
	"strings"
	"testing"
)

// S1-A1 (M-118): the parked ("blocked") status must interrupt the local agent
// but must NEVER count as terminal for GC — the parked workdir and session
// have to survive so the answer's resume run can continue them (Architect
// amendment 2).

func TestShouldInterruptAgentOnParked(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{"blocked", true},   // S1-A1: parked on a human answer → interrupt
		{"completed", true}, // terminal
		{"failed", true},
		{"cancelled", true},
		{"running", false},
		{"dispatched", false},
		{"queued", false},
		{"waiting_local_directory", false},
		{"deferred", false},
		{"", false},
	} {
		if got := shouldInterruptAgent(tc.status, nil); got != tc.want {
			t.Errorf("shouldInterruptAgent(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
	// A 404 (row deleted) still interrupts regardless of status.
	notFound := &requestError{StatusCode: http.StatusNotFound, Body: `{"error":"task not found"}`}
	if !shouldInterruptAgent("", notFound) {
		t.Error("shouldInterruptAgent(404 task not found) = false, want true")
	}
}

func TestIsAgentTaskParked(t *testing.T) {
	if !isAgentTaskParked("blocked") {
		t.Error("isAgentTaskParked(blocked) = false, want true")
	}
	for _, s := range []string{"completed", "failed", "cancelled", "running", "deferred", ""} {
		if isAgentTaskParked(s) {
			t.Errorf("isAgentTaskParked(%q) = true, want false", s)
		}
	}
}

// The GC-facing terminal set must NOT include blocked: a parked task's
// workdir is the resume target, so reaping it would strand the answer run.
func TestIsAgentTaskTerminalExcludesBlocked(t *testing.T) {
	if isAgentTaskTerminal("blocked") {
		t.Fatal("isAgentTaskTerminal(blocked) = true; GC would reap the parked workdir the answer run resumes into")
	}
	for _, s := range []string{"completed", "failed", "cancelled"} {
		if !isAgentTaskTerminal(s) {
			t.Errorf("isAgentTaskTerminal(%q) = false, want true", s)
		}
	}
}

func TestBuildAskPlanResumePrompt(t *testing.T) {
	question := buildAskPlanResumePrompt(Task{IssueID: "42", TriggerEvidenceKind: "question_answer", HandoffNote: "Q1: Which day?\n  - Thursday"})
	for _, want := range []string{"[ANSWER]", "Thursday", "resumes the session", "Your assigned issue ID is: 42"} {
		if !strings.Contains(question, want) {
			t.Errorf("question resume prompt missing %q:\n%s", want, question)
		}
	}

	plan := buildAskPlanResumePrompt(Task{IssueID: "42", TriggerEvidenceKind: "plan_verdict", HandoffNote: `Your plan "Release plan" (revision 3) was APPROVED`})
	for _, want := range []string{"[PLAN VERDICT]", "APPROVED", "resumes the session", "Your assigned issue ID is: 42"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan resume prompt missing %q:\n%s", want, plan)
		}
	}
}
