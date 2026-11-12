package moctmcp

// The tool surface, fixed by docs/moct-seam.md out-path 2. Descriptions are
// written for the agent that will read them, because the only way it learns
// moct-be's write rules (pending approval, near-duplicate holds, the types a
// task home accepts) is what this list tells it.

// toolDescriptors returns the tools/list payload.
func toolDescriptors() []map[string]any {
	return []map[string]any{
		{
			"name":        "memory_search",
			"description": "Search the MOCT team memory visible to this run: workspace-wide conventions and decisions, this agent's own knowledge profile, and this issue's/project's records. Scope tags are OR-ed, so team records appear for every search in the workspace; narrow with q keywords. Use this before asking a question the team may already have answered.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"q": map[string]any{
						"type":        "string",
						"description": "Keywords; every word must match the record's title or body.",
					},
					"types": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filter by record type: rule, decision, convention, how_to_work, task, branch.",
					},
					"homes": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filter by record home kind: task, channel, team, expert.",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "Record status, or \"all\". Empty means moct-be's default (active only).",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Page size, 1..20. Empty means moct-be's default.",
						"minimum":     1,
						"maximum":     20,
					},
					"cursor": map[string]any{
						"type":        "string",
						"description": "nextCursor from a previous search result.",
					},
				},
			},
		},
		{
			"name":        "memory_get",
			"description": "Fetch full MOCT memory records by id (1..10 per call) — for records listed by a search or named in a pre-run inject index. Ids this run may not see come back in \"missing\", never as data.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"ids"},
				"properties": map[string]any{
					"ids": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Record ids, 1 to 10.",
						"minItems":    1,
						"maxItems":    10,
					},
				},
			},
		},
		{
			"name":        "memory_note",
			"description": "Write one record to the MOCT team memory, scoped to this run. Defaults to this issue's task home; workspace and author come from the run's identity and cannot be overridden. Rules: a near-duplicate title holds the write and returns nearMatches without a record — resend with confirmNew:true to write it anyway. Under moct-be's default expertApproval setting, a worker's rule/decision/how_to_work is saved as \"pending\" and does NOT inject into future runs until a human approves it (the response notice says so); a convention activates immediately. A task home accepts type \"decision\" only, and a decision needs attrs.decidedBy and attrs.sourceRef.",
			"inputSchema": map[string]any{
				"type":     "object",
				"required": []string{"type", "title", "body"},
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Record type: decision, convention, rule, how_to_work, task, branch.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Short title. Near-duplicate titles are held for confirmation.",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "The record's content.",
					},
					"attrs": map[string]any{
						"type":        "object",
						"description": "Type-specific attributes, e.g. decidedBy and sourceRef for a decision.",
					},
					"home": map[string]any{
						"type":        "object",
						"description": "{kind, ref} with kind one of task, channel, team, expert. Defaults to this issue's task home.",
						"properties": map[string]any{
							"kind": map[string]any{"type": "string"},
							"ref":  map[string]any{"type": "string"},
						},
					},
					"confirmNew": map[string]any{
						"type":        "boolean",
						"description": "Resend with true to write anyway after a near-duplicate hold.",
					},
				},
			},
		},
	}
}
