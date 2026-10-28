package daemon

// M-140: the run row's init-context report. These tests pin the two things that
// are easy to get wrong at the call site — counting the memory block twice, and
// losing a part because it was not produced.

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/initctx"
)

func briefFixture() execenv.RuntimeBrief {
	return execenv.RuntimeBrief{
		Content: "## brief\n",
		SHA256:  "prefix-hash",
		Parts: []initctx.PartReport{
			{Part: initctx.PartInstructions, Tokens: 4000, Measured: true, Note: "never trimmed"},
			{Part: initctx.PartSkills, Tokens: 1000, Measured: true, Note: "names only"},
			{Part: initctx.PartProject, Tokens: 200, Measured: true, Note: "no project on this task"},
		},
	}
}

// BuildPrompt prepends the memory block verbatim, so the prompt's byte count
// already contains it. Pricing the task part from the whole prompt would count
// those tokens twice and make the memory part's budget meaningless.
func TestNewInitContextDoesNotDoubleCountMemory(t *testing.T) {
	t.Parallel()

	const body = "You are running as a local coding agent for a Multica workspace.\n"
	block := "## MOCT memory (pre-run inject)\n\n#### [convention] Rule\n\nDo the thing.\n\n"

	report := newInitContext(
		"dsh",
		briefFixture(),
		initctx.PartReport{Part: initctx.PartMemory, Tokens: initctx.Estimate(block), Measured: true, Note: "1 entries"},
		block+body,
		block,
		Task{IssueID: "issue-1", TriggerCommentID: "comment-1"},
	)

	byPart := map[initctx.Part]initctx.PartReport{}
	for _, p := range report.Parts {
		byPart[p.Part] = p
	}
	if got, want := byPart[initctx.PartTask].Tokens, initctx.Estimate(body); got != want {
		t.Errorf("task part = %d tokens, want %d (the memory block is a separate part)", got, want)
	}
	if got, want := byPart[initctx.PartMemory].Tokens, initctx.Estimate(block); got != want {
		t.Errorf("memory part = %d tokens, want %d", got, want)
	}
}

func TestNewInitContextRendersEveryPartInOrder(t *testing.T) {
	t.Parallel()

	report := newInitContext("dsh", briefFixture(), initctx.PartReport{Part: initctx.PartMemory, Measured: true, Note: "no memory for this scope"}, "task", "", Task{IssueID: "issue-1"})

	if len(report.Parts) != len(initctx.Order) {
		t.Fatalf("report has %d parts, want %d", len(report.Parts), len(initctx.Order))
	}
	for i, part := range report.Parts {
		if part.Part != initctx.Order[i] {
			t.Errorf("part %d = %q, want %q", i, part.Part, initctx.Order[i])
		}
		if part.Budget != initctx.Budget(part.Part) {
			t.Errorf("%q budget = %d, want %d", part.Part, part.Budget, initctx.Budget(part.Part))
		}
		if part.Note == "" {
			t.Errorf("%q has no note; a part without one cannot say why it is thin", part.Part)
		}
	}
	if report.Provider != "dsh" || report.PrefixSHA != "prefix-hash" {
		t.Errorf("report = %+v, want the provider and the brief's prefix hash", report)
	}
}

func TestTaskSurfaceNamesThePromptShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		task Task
		want string
	}{
		{Task{WakeupID: "w1", IssueID: "i1", TriggerCommentID: "c1"}, "wakeup"},
		{Task{QuickCreatePrompt: "make an issue"}, "quick-create"},
		{Task{AutopilotRunID: "r1", IssueID: "i1"}, "autopilot"},
		{Task{IssueID: "i1", TriggerCommentID: "c1"}, "trigger-comment"},
		{Task{IssueID: "i1"}, "issue-assignment"},
		{Task{ChatSessionID: "s1"}, "chat"},
	}
	for _, tc := range cases {
		if got := taskSurface(tc.task); got != tc.want {
			t.Errorf("taskSurface(%+v) = %q, want %q", tc.task, got, tc.want)
		}
	}
}

func TestMarshalInitContext(t *testing.T) {
	t.Parallel()

	if raw := marshalInitContext(nil); raw != nil {
		t.Fatalf("nil report marshalled to %s, want nil so the field is omitted", raw)
	}

	raw := marshalInitContext(initctx.Finalize("dsh", nil, "abc"))
	if len(raw) == 0 {
		t.Fatal("report marshalled to nothing")
	}
	var decoded struct {
		Schema string `json:"schema"`
		Prefix string `json:"prefix_sha256"`
		Parts  []struct {
			Part string `json:"part"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if decoded.Schema != initctx.Schema || decoded.Prefix != "abc" {
		t.Errorf("decoded = %+v", decoded)
	}
	if len(decoded.Parts) != len(initctx.Order) {
		t.Errorf("decoded %d parts, want %d", len(decoded.Parts), len(initctx.Order))
	}
}
