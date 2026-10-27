package askplanmcp

// The tool surface for S1-A2 (M-119): two tools, ask and plan, mirroring the
// S1-A1 HTTP edge (server/internal/handler/askplan.go) minus the human-only
// actions. Descriptions are written for the agent that reads them, because
// this list is where it learns the non-blocking contract: a submit parks the
// task, and the answer/verdict arrives in the resumed run's prompt.
//
// Limits parity: the numbers below mirror the `const` block at
// handler/askplan.go:39. They are duplicated here (a stdio tool server must
// not link the whole HTTP handler package) and kept honest by a named parity
// test, TestAskPlanMCPToolLimitsMatchHandler, in package handler — the
// server stays authoritative, and that test makes drift visible.

const (
	// AskMaxQuestions is the most questions one card may carry.
	AskMaxQuestions = 6
	// AskMinOptions/AskMaxOptions bound each question's option list.
	AskMinOptions = 2
	AskMaxOptions = 4
	// AskPromptLimit bounds one question's prompt, in characters.
	AskPromptLimit = 300
	// AskContextLimit bounds the card's optional context, in characters.
	AskContextLimit = 1200
	// AskLabelLimit bounds one option label, in characters.
	AskLabelLimit = 40
	// AskDescriptionLimit bounds one option description, in characters.
	AskDescriptionLimit = 200
	// PlanTitleLimit bounds the plan title. It mirrors the literal 200 in
	// handler/askplan.go SubmitPlan — that edge has no named constant, so
	// this one is pinned by the tool's own tests instead of the parity test.
	PlanTitleLimit = 200
	// PlanMaxSections is the most sections a plan outline may declare.
	PlanMaxSections = 40
	// PlanSectionCharMax bounds one section body, in characters.
	PlanSectionCharMax = 20000
)

// toolDescriptors returns the tools/list payload: ask and plan, with every
// schema bound that the server's edge validates, so a well-formed call never
// dies on a limit the JSON schema already stated.
func toolDescriptors() []map[string]any {
	return []map[string]any{
		{
			"name":        "ask",
			"description": "Ask the human a decision question instead of blocking in prose or guessing. One card carries 1 to 6 questions, each with 2 to 4 options (a short label plus a one-sentence description of what happens) and always a written answer field too. The call returns immediately after the card is created: this task then ends its run as blocked (\"awaiting human\") and the answer arrives in the resumed run's prompt — stop working after the call succeeds, before any step no one can undo. A failed call is a tool error and does NOT park the task; fix the argument or carry on without the card.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"questions"},
				"properties": map[string]any{
					"context": map[string]any{
						"type":        "string",
						"maxLength":   AskContextLimit,
						"description": "Optional background the human needs to answer, at most 1200 characters.",
					},
					"questions": map[string]any{
						"type":        "array",
						"minItems":    1,
						"maxItems":    AskMaxQuestions,
						"description": "1 to 6 questions, answered together.",
						"items": map[string]any{
							"type":     "object",
							"required": []string{"prompt", "options"},
							"properties": map[string]any{
								"prompt": map[string]any{
									"type":        "string",
									"minLength":   1,
									"maxLength":   AskPromptLimit,
									"description": "The question, at most 300 characters.",
								},
								"multiple": map[string]any{
									"type":        "boolean",
									"description": "true when several options may be picked together; default false.",
								},
								"options": map[string]any{
									"type":        "array",
									"minItems":    AskMinOptions,
									"maxItems":    AskMaxOptions,
									"description": "2 to 4 options.",
									"items": map[string]any{
										"type":     "object",
										"required": []string{"label"},
										"properties": map[string]any{
											"label": map[string]any{
												"type":        "string",
												"minLength":   1,
												"maxLength":   AskLabelLimit,
												"description": "One or two words, at most 40 characters.",
											},
											"description": map[string]any{
												"type":        "string",
												"maxLength":   AskDescriptionLimit,
												"description": "One sentence on what happens if it is picked, at most 200 characters.",
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			"name":        "plan",
			"description": "Propose a plan for this issue and, once the human approves it, the plan becomes the issue description. Workflow: create with a title and an outline of section headings; write one section per call (section_id to replace one, omit it to append), each write fenced by the plan revision the previous call returned; submit when the plan is ready. Only one of create/write/submit runs per call. The call returns immediately after create/submit — submitting parks this task as blocked (\"awaiting human\") and the verdict (approved, changes requested with comments, or cancel) arrives in the resumed run's prompt. A stale base_revision writes nothing and returns the current revision in the tool error; retry with it. The gate: the issue must still be backlog, todo or planing_2.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"action"},
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []string{"create", "write", "submit"},
						"description": "create: start a plan. write: replace or append one section. submit: send the plan for review.",
					},
					"title": map[string]any{
						"type":        "string",
						"minLength":   1,
						"maxLength":   PlanTitleLimit,
						"description": "create: the plan title, at most 200 characters.",
					},
					"outline": map[string]any{
						"type":        "array",
						"maxItems":    PlanMaxSections,
						"items":       map[string]any{"type": "string"},
						"description": "create: section headings in order, at most 40; each starts as an empty section.",
					},
					"plan_id": map[string]any{
						"type":        "string",
						"description": "write and submit: the plan id exactly as returned by create or a previous response.",
					},
					"base_revision": map[string]any{
						"type":        "integer",
						"description": "write: the plan revision your latest response rendered. A stale value writes nothing and the error carries the current revision.",
					},
					"section_id": map[string]any{
						"type":        "string",
						"description": "write: the section to replace ('s1', 's2', …). Omit to append a new section.",
					},
					"heading": map[string]any{
						"type":        "string",
						"description": "write: the section heading.",
					},
					"markdown": map[string]any{
						"type":        "string",
						"maxLength":   PlanSectionCharMax,
						"description": "write: the whole new section body in Markdown, at most 20000 characters. It replaces the previous body.",
					},
					"revision": map[string]any{
						"type":        "integer",
						"description": "submit: the plan revision you are asking the human to review — the one your latest write rendered.",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "submit: an optional one-paragraph note for the reviewer.",
					},
				},
			},
		},
	}
}
