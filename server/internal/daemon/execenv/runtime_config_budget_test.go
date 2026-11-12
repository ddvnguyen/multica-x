package execenv

// M-140 budget tests: the brief is the largest part of a run's first request,
// so what the builder puts in it, what it cuts, and what it says about the cut
// are all part of the contract.

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/daemon/initctx"
)

func skillPart(t *testing.T, brief RuntimeBrief) initctx.PartReport {
	t.Helper()
	for _, p := range brief.Parts {
		if p.Part == initctx.PartSkills {
			return p
		}
	}
	t.Fatalf("brief carries no skills part: %+v", brief.Parts)
	return initctx.PartReport{}
}

func projectPart(t *testing.T, brief RuntimeBrief) initctx.PartReport {
	t.Helper()
	for _, p := range brief.Parts {
		if p.Part == initctx.PartProject {
			return p
		}
	}
	t.Fatalf("brief carries no project part: %+v", brief.Parts)
	return initctx.PartReport{}
}

// Only dsh gets skill descriptions. Every other runtime's CLI already lists its
// own descriptions, so growing the brief for them would be bytes spent twice —
// and would break the byte-for-byte brief those runtimes have today.
func TestSkillDescriptionsAreDshOnly(t *testing.T) {
	t.Parallel()

	const description = "Use this when the change touches a database migration."
	ctx := TaskContextForEnv{
		IssueID: "issue-1",
		AgentSkills: []SkillContextForEnv{
			{Name: "Migration Safety", Description: description, Content: "body"},
		},
	}

	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{"dsh", true},
		{"claude", false},
		{"codex", false},
		{"opencode", false},
		{"kiro", false},
	} {
		tc := tc
		t.Run(tc.provider, func(t *testing.T) {
			t.Parallel()
			brief, err := InjectRuntimeConfig(t.TempDir(), tc.provider, ctx)
			if err != nil {
				t.Fatalf("InjectRuntimeConfig: %v", err)
			}
			has := strings.Contains(brief.Content, description)
			if has != tc.want {
				t.Errorf("%s brief carries descriptions = %v, want %v", tc.provider, has, tc.want)
			}
			// The name is in the index either way.
			if !strings.Contains(brief.Content, "- **migration-safety**") {
				t.Errorf("%s brief lost the skill name", tc.provider)
			}
			if part := skillPart(t, brief); part.Note == "" {
				t.Errorf("%s skills part must explain itself: %+v", tc.provider, part)
			}
		})
	}
}

// A workspace with more description text than the part's budget allows keeps
// every skill NAME and drops descriptions from the tail, naming what lost one.
func TestSkillDescriptionsDropTailUnderBudget(t *testing.T) {
	t.Parallel()

	// Enough skills that even the shortest description cap overflows the part's
	// budget: this is the branch that has to drop descriptions from the tail.
	const skills = 200
	long := strings.Repeat("This skill applies to every migration. ", 40)
	ctx := TaskContextForEnv{IssueID: "issue-1"}
	for i := 0; i < skills; i++ {
		ctx.AgentSkills = append(ctx.AgentSkills, SkillContextForEnv{
			Name:        fmt.Sprintf("Skill %03d", i),
			Description: long,
			Content:     "body",
		})
	}

	brief, err := InjectRuntimeConfig(t.TempDir(), "dsh", ctx)
	if err != nil {
		t.Fatalf("InjectRuntimeConfig: %v", err)
	}
	part := skillPart(t, brief)
	if !part.Trimmed {
		t.Fatalf("%d long descriptions must not fit the skills budget: %+v", skills, part)
	}
	if len(part.Dropped) == 0 {
		t.Fatalf("a trimmed part must name what it dropped: %+v", part)
	}
	if part.Tokens > part.Budget {
		t.Errorf("trimmed skills part is still over budget: %d/%d", part.Tokens, part.Budget)
	}
	// Every name survives, dropped or not: the model can still invoke it.
	for i := 0; i < skills; i++ {
		name := fmt.Sprintf("- **skill-%03d**", i)
		if !strings.Contains(brief.Content, name) {
			t.Errorf("brief lost skill name %q", name)
		}
	}
}

// The project description is owner-authored prose of unbounded length, so it is
// the one field that can blow the project part on its own. It is cut at a byte
// cap on a rune boundary; the resources are never cut, because they carry the
// checkout targets the task depends on.
func TestProjectDescriptionCutOnRuneBoundary(t *testing.T) {
	t.Parallel()

	// 3 bytes per rune, so a naive byte cut would land mid-rune.
	description := strings.Repeat("日本語", (projectDescriptionMaxBytes/3)+500)
	ctx := TaskContextForEnv{
		IssueID:            "issue-1",
		ProjectID:          "project-1",
		ProjectTitle:       "Big Project",
		ProjectDescription: description,
		ProjectResources: []ProjectResourceForEnv{
			{ID: "r1", ResourceType: "github_repo", ResourceRef: []byte(`{"url":"https://github.com/org/big"}`)},
		},
	}

	brief, err := InjectRuntimeConfig(t.TempDir(), "claude", ctx)
	if err != nil {
		t.Fatalf("InjectRuntimeConfig: %v", err)
	}
	if !utf8.ValidString(brief.Content) {
		t.Fatal("a byte-bounded cut split a rune and left invalid UTF-8 in the brief")
	}
	if len(brief.Content) >= len(description) {
		t.Fatal("the description was not cut at all")
	}
	if !strings.Contains(brief.Content, "…") {
		t.Error("a cut description must be marked with an ellipsis")
	}
	for _, want := range []string{"https://github.com/org/big", "Big Project"} {
		if !strings.Contains(brief.Content, want) {
			t.Errorf("the cut dropped %q, which must survive", want)
		}
	}
	part := projectPart(t, brief)
	if !part.Trimmed {
		t.Errorf("a cut project part must be flagged trimmed: %+v", part)
	}
	if !strings.Contains(part.Note, "cut to stay inside this part's budget") {
		t.Errorf("the note must say the part is thin because content was cut, got %q", part.Note)
	}
}

// A thin project part has to name its reason: an empty project, a project with
// resources but no prose, and a short prose are three different facts.
func TestProjectNoteNamesWhyThePartIsThin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ctx  TaskContextForEnv
		want string
	}{
		{
			name: "no project",
			ctx:  TaskContextForEnv{IssueID: "issue-1"},
			want: "no project on this task",
		},
		{
			name: "empty project",
			ctx:  TaskContextForEnv{IssueID: "issue-1", ProjectID: "p1", ProjectTitle: "Empty"},
			want: "no description and no resources",
		},
		{
			name: "resources only",
			ctx: TaskContextForEnv{
				IssueID:   "issue-1",
				ProjectID: "p1",
				ProjectResources: []ProjectResourceForEnv{
					{ID: "r1", ResourceType: "github_repo", ResourceRef: []byte(`{"url":"https://github.com/org/x"}`)},
				},
			},
			want: "resources only",
		},
		{
			name: "short prose",
			ctx:  TaskContextForEnv{IssueID: "issue-1", ProjectID: "p1", ProjectDescription: "Short."},
			want: "the project description is short",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			brief, err := InjectRuntimeConfig(t.TempDir(), "claude", tc.ctx)
			if err != nil {
				t.Fatalf("InjectRuntimeConfig: %v", err)
			}
			part := projectPart(t, brief)
			if !strings.Contains(part.Note, tc.want) {
				t.Errorf("note = %q, want it to contain %q", part.Note, tc.want)
			}
		})
	}
}

// The card's own evidence at unit level: two fresh sessions on identical input
// must produce the same prefix fingerprint, and any change to the brief must
// move it. This is what makes "byte-stable prefix" checkable without a provider
// key.
func TestRuntimeBriefPrefixHashIsStableAndSensitive(t *testing.T) {
	t.Parallel()

	ctx := TaskContextForEnv{
		IssueID:            "issue-1",
		ProjectID:          "project-1",
		ProjectTitle:       "Stable Project",
		ProjectDescription: "Durable context that does not change between runs.",
	}

	first, err := InjectRuntimeConfig(t.TempDir(), "dsh", ctx)
	if err != nil {
		t.Fatalf("first InjectRuntimeConfig: %v", err)
	}
	// A second, independent workdir stands in for a second fresh session.
	second, err := InjectRuntimeConfig(t.TempDir(), "dsh", ctx)
	if err != nil {
		t.Fatalf("second InjectRuntimeConfig: %v", err)
	}
	if first.SHA256 == "" {
		t.Fatal("brief carries no prefix hash")
	}
	if first.SHA256 != second.SHA256 {
		t.Errorf("prefix hash drifted across two fresh sessions:\n%s\n%s", first.SHA256, second.SHA256)
	}
	if first.Content != second.Content {
		t.Error("brief bytes drifted across two fresh sessions")
	}

	changed := ctx
	changed.ProjectDescription += " (revised)"
	third, err := InjectRuntimeConfig(t.TempDir(), "dsh", changed)
	if err != nil {
		t.Fatalf("third InjectRuntimeConfig: %v", err)
	}
	if third.SHA256 == first.SHA256 {
		t.Error("a changed brief must change the prefix hash")
	}
}

// The parts must describe the bytes actually written: their sum is the brief.
func TestBriefPartsAccountForEveryByte(t *testing.T) {
	t.Parallel()

	ctx := TaskContextForEnv{
		IssueID:      "issue-1",
		ProjectID:    "project-1",
		ProjectTitle: "Accounted",
		AgentSkills:  []SkillContextForEnv{{Name: "One", Description: "d", Content: "body"}},
	}
	brief, err := InjectRuntimeConfig(t.TempDir(), "dsh", ctx)
	if err != nil {
		t.Fatalf("InjectRuntimeConfig: %v", err)
	}

	sum := 0
	for _, part := range brief.Parts {
		sum += part.Tokens
	}
	// The three brief parts are byte ranges of one buffer, so together they are
	// the whole brief. Estimate rounding is per part, hence the tolerance.
	if diff := sum - initctx.EstimateBytes(len(brief.Content)); diff < -len(brief.Parts) || diff > len(brief.Parts) {
		t.Errorf("part tokens sum to %d, brief estimates %d", sum, initctx.EstimateBytes(len(brief.Content)))
	}
}
