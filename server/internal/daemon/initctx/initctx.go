// Package initctx models the context a run sends to its provider on the first
// request: the ordered parts it is built from, the token budget each part gets,
// and the per-part report the daemon records on the run row (M-140).
//
// The part model exists so the 20–30 K target is measurable instead of
// asserted. Every part carries its own budget, its estimated size, whether it
// was trimmed to fit, and — when it lands under budget — why it is thin. A run
// whose context is small because the workspace has no MOCT memory is a
// different fact from a run whose context is small because the builder dropped
// content, and the report has to tell them apart.
//
// Token counts are ESTIMATES: (bytes+3)/4, the same chars-per-token rule the
// M-133 spike used for its token meter, so these numbers stay comparable with
// the measured 13,315-token first request. Only a provider's own usage frame
// prices a request exactly, and that number is not attributable to a part.
package initctx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Part names one slice of the first request. The set is closed: adding a part
// means adding a budget below and a producer at the call site, and the report
// renders parts in Order so two runs are diffable line by line.
type Part string

const (
	// PartInstructions is the runtime brief (AGENTS.md / CLAUDE.md) minus the
	// skills index and the project block: identity, workspace context, the
	// command catalogue, and the workflow.
	PartInstructions Part = "instructions"
	// PartTools is the provider's own tool-schema block. The daemon cannot see
	// it; see ReferenceToolTokens.
	PartTools Part = "tools"
	// PartSkills is the skills index in the brief.
	PartSkills Part = "skills"
	// PartMemory is the MOCT pre-run memory block prepended to the task prompt.
	PartMemory Part = "memory"
	// PartProject is the project context block inside the brief.
	PartProject Part = "project"
	// PartTask is the per-turn task prompt: the trigger, the issue id, and the
	// run-scoped blocks appended after the cached prefix.
	PartTask Part = "task"
	// PartSeed is the summary+trigger text a seeded dsh session inherits
	// (S3-B2/M-138). Zero tokens when the run resumed or started fresh; the
	// daemon fills it through SeedPartFor when it composed one.
	PartSeed Part = "seed"
)

// Order is the canonical rendering order: static content first, run-scoped
// content last. It is the report's order too, so a reader can compare two run
// rows without re-sorting.
var Order = []Part{
	PartInstructions,
	PartTools,
	PartSkills,
	PartMemory,
	PartProject,
	PartTask,
	PartSeed,
}

// Budgets in estimated tokens. They sum to 27 K, the middle of the Owner's
// 20–30 K target: a typical run should land inside the band without any part
// being pushed past its own ceiling.
//
// A budget is a ceiling, not a target. Padding a part to reach it would spend
// the provider's money on tokens the agent does not need, so a part that is
// genuinely small stays small and says why (see PartReport.Note).
const (
	BudgetInstructions = 4000
	BudgetTools        = 6000
	BudgetSkills       = 3000
	BudgetMemory       = 6000
	BudgetProject      = 3000
	BudgetTask         = 3000
	BudgetSeed         = 2000
)

// BandMin and BandMax are the Owner's target for the sum of the seven parts.
// The provider envelope (dsh_session_log, dsh_plugin_packages) is deliberately
// outside this band: it is assembled by the bridge, priced by the provider, and
// reported separately as priced:unknown until M-126 can measure it.
const (
	BandMin = 20000
	BandMax = 30000
)

const (
	// Schema versions the report shape on the run row.
	Schema = "initctx/1"
	// EstimateRule names the estimator so a reader never has to guess whether
	// a number is exact.
	EstimateRule = "(bytes+3)/4"
)

// dshToolSchemaReferenceTokens is the last measured cost of dsh's own tool
// schemas: 34 schemas, 7,504 tokens, from the M-133 spike's check 8a on lane z.
//
// It is a REFERENCE, not a measurement: the daemon never sees the provider's
// tool definitions, so it cannot count them. It is reported with
// Measured=false so nobody mistakes it for live data, and it is scoped to dsh
// because that is the runtime it was measured on. Replace it with a real number
// when a runtime starts reporting its own schema cost.
const dshToolSchemaReferenceTokens = 7504

// Budget returns the token budget for a part, or 0 for an unknown part.
func Budget(p Part) int {
	switch p {
	case PartInstructions:
		return BudgetInstructions
	case PartTools:
		return BudgetTools
	case PartSkills:
		return BudgetSkills
	case PartMemory:
		return BudgetMemory
	case PartProject:
		return BudgetProject
	case PartTask:
		return BudgetTask
	case PartSeed:
		return BudgetSeed
	default:
		return 0
	}
}

// EstimateBytes converts a byte count to estimated tokens with the (bytes+3)/4
// rule. Negative and zero sizes are zero tokens.
func EstimateBytes(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 3) / 4
}

// Estimate is EstimateBytes over a string.
func Estimate(s string) int { return EstimateBytes(len(s)) }

// Hash is the hex SHA-256 of s, used for the prefix fingerprint.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// PartReport is one part's line on the run row.
type PartReport struct {
	Part   Part `json:"part"`
	Tokens int  `json:"tokens"`
	Budget int  `json:"budget"`
	// Over is true when Tokens exceeds Budget. An over part is never silently
	// trimmed: instructions and task are never cut at all, and the others are
	// only cut by their own rule, so an overshoot here is a fact to fix at the
	// source, not a number to hide.
	Over bool `json:"over,omitempty"`
	// Measured is false when the token count is a reference figure rather than
	// something this run computed. Today only tools is unmeasured.
	Measured bool `json:"measured"`
	// Trimmed is true when this part's producer dropped or shortened content to
	// fit the budget. Dropped names what went, so a reader can judge whether the
	// loss mattered.
	Trimmed bool     `json:"trimmed,omitempty"`
	Dropped []string `json:"dropped,omitempty"`
	// Source names the producer when it is not this daemon ("runtime").
	Source string `json:"source,omitempty"`
	// Note explains a thin or unmeasured part. A part under budget without a
	// note is a report bug, not a small part: the reader cannot tell "nothing to
	// say" from "we dropped it".
	Note string `json:"note,omitempty"`
}

// Envelope is the provider-side wrapper around the parts. It is priced
// separately because it is not ours: dsh assembles dsh_session_log and
// dsh_plugin_packages in the bridge, and only the provider can say whether they
// are billed.
type Envelope struct {
	Priced string `json:"priced"`
	Note   string `json:"note,omitempty"`
}

// Report is the run row's init_context object.
type Report struct {
	Schema    string       `json:"schema"`
	Provider  string       `json:"provider,omitempty"`
	Total     int          `json:"total_tokens"`
	Measured  int          `json:"measured_tokens"`
	Estimate  string       `json:"estimate_rule"`
	PrefixSHA string       `json:"prefix_sha256"`
	Band      Band         `json:"band"`
	InBand    bool         `json:"in_band"`
	Parts     []PartReport `json:"parts"`
	Envelope  Envelope     `json:"envelope"`
	// Note states which parts are thin and why when the total is outside the
	// band, so an out-of-band run explains itself without a second read.
	Note string `json:"note,omitempty"`
}

// Band is the target range, carried on the row so a reader never needs this
// package's constants to interpret the number.
type Band struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Finalize fills in budgets, overage, totals and the band verdict, and orders
// the parts canonically. Parts the caller omitted are rendered as zero-token
// entries so the table always has seven rows — a missing row would read as
// "this part does not exist" instead of "this part was empty".
func Finalize(provider string, parts []PartReport, prefixSHA256 string) *Report {
	byPart := make(map[Part]PartReport, len(parts))
	for _, p := range parts {
		byPart[p.Part] = p
	}
	ordered := make([]PartReport, 0, len(Order))
	total, measured := 0, 0
	for _, part := range Order {
		p, ok := byPart[part]
		if !ok {
			p = PartReport{Part: part, Measured: true}
		}
		p.Part = part
		p.Budget = Budget(part)
		p.Over = p.Budget > 0 && p.Tokens > p.Budget
		ordered = append(ordered, p)
		total += p.Tokens
		if p.Measured {
			measured += p.Tokens
		}
	}
	r := &Report{
		Schema:    Schema,
		Provider:  provider,
		Total:     total,
		Measured:  measured,
		Estimate:  EstimateRule,
		PrefixSHA: prefixSHA256,
		Band:      Band{Min: BandMin, Max: BandMax},
		InBand:    total >= BandMin && total <= BandMax,
		Parts:     ordered,
		Envelope: Envelope{
			Priced: "unknown",
			Note:   "dsh_session_log and dsh_plugin_packages are assembled by the bridge and priced by the provider; the daemon cannot measure them (M-126).",
		},
	}
	if !r.InBand {
		r.Note = r.bandNote()
	}
	return r
}

// bandNote explains a total outside the band by naming the thin parts and
// repeating what their producers said about them.
func (r *Report) bandNote() string {
	var thin []string
	for _, p := range r.Parts {
		if p.Budget == 0 || p.Tokens*2 >= p.Budget {
			continue
		}
		entry := fmt.Sprintf("%s %d/%d", p.Part, p.Tokens, p.Budget)
		if p.Note != "" {
			entry += " (" + p.Note + ")"
		}
		thin = append(thin, entry)
	}
	direction := "below"
	gap := BandMin - r.Total
	if r.Total > BandMax {
		direction = "above"
		gap = r.Total - BandMax
	}
	if len(thin) == 0 {
		return fmt.Sprintf("%s band by %d tokens", direction, gap)
	}
	return fmt.Sprintf("%s band by %d tokens; thin parts: %s", direction, gap, strings.Join(thin, ", "))
}

// ToolPart returns the tools row for a provider. The daemon cannot see the
// runtime's schemas, so this row is always unmeasured; dsh carries the last
// real measurement, every other runtime carries none.
func ToolPart(provider string) PartReport {
	if provider != "dsh" {
		return PartReport{
			Part:     PartTools,
			Measured: false,
			Source:   "runtime",
			Note:     "tool schemas are the runtime's; no reference measurement for this provider",
		}
	}
	return PartReport{
		Part:     PartTools,
		Tokens:   dshToolSchemaReferenceTokens,
		Measured: false,
		Source:   "runtime",
		Note:     "reference figure from M-133 check 8a (34 dsh schemas); the daemon cannot count the runtime's schemas",
	}
}

// SeedPart returns the seed row for a run that did not start from a summary.
func SeedPart() PartReport {
	return PartReport{
		Part:     PartSeed,
		Measured: true,
		Note:     "no seed: this run resumed a session or started fresh",
	}
}

// SeedPartFor returns the seed row for a run whose session was seeded from a
// summary (S3-B2/M-138). The seed is composed by the daemon, so the count is
// this run's own estimate; trimmed and dropped say what the 32K cap cost.
func SeedPartFor(tokens int, trimmed bool, dropped []string, note string) PartReport {
	return PartReport{
		Part:     PartSeed,
		Tokens:   tokens,
		Measured: true,
		Trimmed:  trimmed,
		Dropped:  dropped,
		Note:     note,
	}
}
