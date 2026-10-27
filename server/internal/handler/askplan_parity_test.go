package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/askplanmcp"
)

// TestAskPlanMCPToolLimitsMatchHandler pins the S1-A2 tool server's limit
// constants to this file's `const` block (the authority). askplanmcp cannot
// link package handler in production — it would drag the whole HTTP stack
// into the CLI binary — so the numbers are duplicated there and this named
// parity test makes drift a test failure instead of an edge where the MCP
// schema advertises one limit and validateAskRequest enforces another.
// PlanTitleLimit is deliberately absent: its counterpart is a literal 200 in
// SubmitPlan with no named constant to compare against.
func TestAskPlanMCPToolLimitsMatchHandler(t *testing.T) {
	cases := []struct {
		name    string
		handler int
		tool    int
	}{
		{"askMaxQuestions", askMaxQuestions, askplanmcp.AskMaxQuestions},
		{"askMinOptions", askMinOptions, askplanmcp.AskMinOptions},
		{"askMaxOptions", askMaxOptions, askplanmcp.AskMaxOptions},
		{"askPromptLimit", askPromptLimit, askplanmcp.AskPromptLimit},
		{"askContextLimit", askContextLimit, askplanmcp.AskContextLimit},
		{"askLabelLimit", askLabelLimit, askplanmcp.AskLabelLimit},
		{"askDescriptionLimit", askDescriptionLimit, askplanmcp.AskDescriptionLimit},
		{"planMaxSections", planMaxSections, askplanmcp.PlanMaxSections},
		{"planSectionCharMax", planSectionCharMax, askplanmcp.PlanSectionCharMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.handler != tc.tool {
				t.Errorf("%s = %d on the tool side but %d in handler/askplan.go — the MCP schema and the server edge must agree",
					tc.name, tc.tool, tc.handler)
			}
		})
	}
}
