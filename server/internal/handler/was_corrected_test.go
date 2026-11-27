package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestPlanVerdictWasCorrected pins M-604 trigger 2 end to end, including the
// Architect's explicit negative: changes_requested stamps was_corrected on
// the resume run's context, approve and cancel do not.
func TestPlanVerdictWasCorrected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict string
		body    map[string]any
		want    bool
	}{
		{"changes_requested is a correction", "changes_requested",
			map[string]any{"comments": []string{"Tighten the rollback step"}}, true},
		{"approve is not a correction", "approve", map[string]any{}, false},
		{"cancel is not a correction", "cancel", map[string]any{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAskPlanFixture(t)
			planID, rev := submittedPlan(t, f)
			body := map[string]any{"revision": rev, "verdict": tc.verdict}
			for k, v := range tc.body {
				body[k] = v
			}
			verdictReq := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/verdict", body)
			testutil.Call(t, testHandler.ReviewPlan,
				withURLParams(verdictReq, "id", testWorkspaceID, "planId", planID),
			).Want(http.StatusOK)
			var context string
			dbfx.QueryRow(t, `SELECT COALESCE(context::text,'{}') FROM agent_task_queue WHERE issue_id=$1 AND status='queued' AND rerun_of_task_id IS NOT NULL`, f.issueID).Scan(&context)
			var decoded struct {
				Corrected bool `json:"was_corrected"`
			}
			if err := json.Unmarshal([]byte(context), &decoded); err != nil {
				t.Fatalf("resume context not JSON: %q", context)
			}
			if decoded.Corrected != tc.want {
				t.Errorf("verdict %q: was_corrected = %v, want %v (context %s)", tc.verdict, decoded.Corrected, tc.want, context)
			}
		})
	}
}

// TestTaskToResponseWasCorrected pins the claim mapping: a stamped task
// carries WasCorrected on the wire, an ordinary one does not.
func TestTaskToResponseWasCorrected(t *testing.T) {
	stamped := db.AgentTaskQueue{Context: []byte(`{"was_corrected":true}`)}
	if resp := taskToResponse(stamped, "ws"); !resp.WasCorrected {
		t.Errorf("stamped task: WasCorrected = false, want true")
	}
	for name, raw := range map[string][]byte{
		"empty": []byte(`{}`),
		"null":  nil,
		"fresh": []byte(`{"fresh_rerun":true}`),
	} {
		if resp := taskToResponse(db.AgentTaskQueue{Context: raw}, "ws"); resp.WasCorrected {
			t.Errorf("%s context: WasCorrected = true, want false", name)
		}
	}
}
