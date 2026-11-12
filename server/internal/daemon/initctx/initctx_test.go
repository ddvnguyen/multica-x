package initctx

import (
	"strings"
	"testing"
)

// The budget table is the number the whole card is about: every part has a
// ceiling, and the ceilings sum to a total inside the Owner's 20–30 K band so a
// typical run can land in band without any single part being pushed over.
func TestBudgetTableCoversEveryPart(t *testing.T) {
	seen := make(map[Part]bool, len(Order))
	total := 0
	for _, part := range Order {
		if seen[part] {
			t.Fatalf("%q appears twice in Order", part)
		}
		seen[part] = true
		budget := Budget(part)
		if budget <= 0 {
			t.Errorf("%q has no budget", part)
		}
		total += budget
	}
	if len(Order) != 7 {
		t.Fatalf("Order has %d parts, want the card's seven", len(Order))
	}
	if total < BandMin || total > BandMax {
		t.Errorf("budgets sum to %d, outside the %d–%d band", total, BandMin, BandMax)
	}
	if Budget(Part("nope")) != 0 {
		t.Error("an unknown part must have no budget")
	}
}

func TestEstimateBytes(t *testing.T) {
	cases := []struct {
		bytes int
		want  int
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{3, 1},
		{4, 1},
		{5, 2},
		{4000, 1000},
		{4001, 1001},
	}
	for _, tc := range cases {
		if got := EstimateBytes(tc.bytes); got != tc.want {
			t.Errorf("EstimateBytes(%d) = %d, want %d", tc.bytes, got, tc.want)
		}
	}
	if got, want := Estimate("abcd"), 1; got != want {
		t.Errorf("Estimate = %d, want %d", got, want)
	}
}

func TestFinalizeOrdersPartsAndFillsBudgets(t *testing.T) {
	// Deliberately supplied out of order and with one part missing.
	report := Finalize("dsh", []PartReport{
		{Part: PartTask, Tokens: 1500, Measured: true},
		{Part: PartInstructions, Tokens: 3000, Measured: true},
	}, "abc")

	if report.Schema != Schema || report.Estimate != EstimateRule {
		t.Errorf("report header = %q/%q", report.Schema, report.Estimate)
	}
	if report.Provider != "dsh" || report.PrefixSHA != "abc" {
		t.Errorf("provider/prefix = %q/%q", report.Provider, report.PrefixSHA)
	}
	if len(report.Parts) != len(Order) {
		t.Fatalf("report has %d parts, want every part in Order rendered", len(report.Parts))
	}
	for i, part := range report.Parts {
		if part.Part != Order[i] {
			t.Errorf("part %d = %q, want %q", i, part.Part, Order[i])
		}
		if part.Budget != Budget(part.Part) {
			t.Errorf("%q budget = %d, want %d", part.Part, part.Budget, Budget(part.Part))
		}
	}
	if report.Total != 4500 {
		t.Errorf("total = %d, want 4500", report.Total)
	}
	if report.Measured != 4500 {
		t.Errorf("measured = %d, want 4500 (both supplied parts are measured)", report.Measured)
	}
}

func TestFinalizeFlagsOverage(t *testing.T) {
	report := Finalize("dsh", []PartReport{
		{Part: PartInstructions, Tokens: BudgetInstructions + 1, Measured: true},
		{Part: PartTools, Tokens: dshToolSchemaReferenceTokens, Measured: false},
	}, "")

	byPart := map[Part]PartReport{}
	for _, p := range report.Parts {
		byPart[p.Part] = p
	}
	if !byPart[PartInstructions].Over {
		t.Error("a part above its budget must be flagged over, not trimmed silently")
	}
	if !byPart[PartTools].Over {
		t.Error("the dsh tool reference is above its budget and must say so")
	}
	if byPart[PartInstructions].Trimmed {
		t.Error("instructions are never trimmed; overage is flagged instead")
	}
	// The unmeasured part counts toward the total but not toward the measured
	// subtotal, so a reader can see how much of the number is a reference.
	if report.Measured != BudgetInstructions+1 {
		t.Errorf("measured = %d, want only the measured part", report.Measured)
	}
	if report.Total != BudgetInstructions+1+dshToolSchemaReferenceTokens {
		t.Errorf("total = %d, want both parts", report.Total)
	}
}

func TestFinalizeRendersOmittedPartsAsZeroRows(t *testing.T) {
	report := Finalize("claude", nil, "")
	if len(report.Parts) != len(Order) {
		t.Fatalf("an empty run rendered %d parts, want %d", len(report.Parts), len(Order))
	}
	for _, p := range report.Parts {
		if p.Tokens != 0 || p.Over || p.Trimmed {
			t.Errorf("%q = %+v, want a plain zero row", p.Part, p)
		}
	}
	if report.Total != 0 || report.Measured != 0 {
		t.Errorf("empty run totals = %d/%d, want zero", report.Total, report.Measured)
	}
}

// The card's requirement: a run outside the band states which parts are thin
// and repeats what their producers said about them.
func TestFinalizeBandNoteNamesThinPartsAndTheirReasons(t *testing.T) {
	report := Finalize("dsh", []PartReport{
		{Part: PartInstructions, Tokens: 3000, Measured: true},
		ToolPart("dsh"),
		{Part: PartSkills, Tokens: 2000, Measured: true},
		{Part: PartMemory, Tokens: 0, Measured: true, Note: "moct returned no entries for this run's scope"},
		{Part: PartProject, Tokens: 1000, Measured: true, Note: "project has no description; resources only"},
		{Part: PartTask, Tokens: 1500, Measured: true},
		SeedPart(),
	}, "deadbeef")

	if report.InBand {
		t.Fatalf("total %d should be below the band", report.Total)
	}
	for _, want := range []string{
		"below band by 4996 tokens",
		"memory 0/6000 (moct returned no entries for this run's scope)",
		"project 1000/3000 (project has no description; resources only)",
		"seed 0/2000",
	} {
		if !strings.Contains(report.Note, want) {
			t.Errorf("band note %q missing %q", report.Note, want)
		}
	}
	// A part at or above half its budget is not thin and must not be listed.
	if strings.Contains(report.Note, "instructions") {
		t.Errorf("band note lists a part that is not thin: %q", report.Note)
	}
	if report.Band.Min != BandMin || report.Band.Max != BandMax {
		t.Errorf("band carried on the row = %+v", report.Band)
	}
	if report.Envelope.Priced != "unknown" {
		t.Errorf("envelope priced = %q, want unknown until a real key prices it", report.Envelope.Priced)
	}
}

func TestFinalizeInBandCarriesNoNote(t *testing.T) {
	report := Finalize("dsh", []PartReport{
		{Part: PartInstructions, Tokens: 4000, Measured: true},
		{Part: PartTools, Tokens: 6000, Measured: true},
		{Part: PartSkills, Tokens: 3000, Measured: true},
		{Part: PartMemory, Tokens: 6000, Measured: true},
		{Part: PartProject, Tokens: 3000, Measured: true},
		{Part: PartTask, Tokens: 3000, Measured: true},
		{Part: PartSeed, Tokens: 2000, Measured: true},
	}, "abc")

	if !report.InBand || report.Total != 27000 {
		t.Fatalf("in-band report = %+v", report)
	}
	if report.Note != "" {
		t.Errorf("an in-band run needs no explanation, got %q", report.Note)
	}
}

func TestToolPart(t *testing.T) {
	dsh := ToolPart("dsh")
	if dsh.Tokens != dshToolSchemaReferenceTokens || dsh.Measured {
		t.Errorf("dsh tools part = %+v, want the unmeasured reference figure", dsh)
	}
	if dsh.Source != "runtime" || dsh.Note == "" {
		t.Errorf("dsh tools part must name its source and say why: %+v", dsh)
	}

	other := ToolPart("claude")
	if other.Tokens != 0 || other.Measured || other.Note == "" {
		t.Errorf("a provider with no reference must report zero and say so: %+v", other)
	}
}

func TestSeedPartIsReservedAndNamed(t *testing.T) {
	seed := SeedPart()
	if seed.Part != PartSeed || seed.Tokens != 0 || seed.Note == "" {
		t.Errorf("seed part = %+v, want a zero row that says it is reserved", seed)
	}
}

func TestHashIsStableAndDistinct(t *testing.T) {
	const a = "## Instructions\n\nhello\n"
	if Hash(a) != Hash(a) {
		t.Fatal("Hash is not deterministic")
	}
	if len(Hash(a)) != 64 {
		t.Fatalf("Hash = %q, want 64 hex chars", Hash(a))
	}
	if Hash(a) == Hash(a+"x") {
		t.Fatal("Hash collided on a one-byte change")
	}
}
