package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestRecordTaskSummaryDecisionBodyIsCapped: the summary-decision endpoint
// rejects a body above its 64 KiB cap with 400 and writes nothing, while a
// normal decision is recorded on the run row.
func TestRecordTaskSummaryDecisionBodyIsCapped(t *testing.T) {
	f := newSupplementFixture(t, "codex", "running", false)
	record := func(body map[string]any) *testutil.Response {
		req := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/summary-decision", body), "taskId", f.taskID)
		return testutil.Call(t, testHandler.RecordTaskSummaryDecision, req)
	}
	recorded := func() int {
		return dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND result ? 'dsh_summary'`, f.taskID)
	}

	// Valid JSON, so only the size cap can explain a 400.
	record(map[string]any{
		"decision": "failed", "session_id": "session-1", "error": strings.Repeat("x", 70<<10),
	}).Want(http.StatusBadRequest)
	if recorded() != 0 {
		t.Fatal("an oversized decision was persisted")
	}

	record(map[string]any{
		"decision": "skipped-below-trigger", "session_id": "session-1", "tokens_above_init": 500,
	}).Want(http.StatusNoContent)
	if recorded() != 1 {
		t.Fatal("a normal decision was not persisted")
	}
}
