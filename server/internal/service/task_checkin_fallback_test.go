package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// HasWakeupCheckin reads the run's context marker that ends the run "with no
// real update": the wakeup marker (wakeup runs only) and, since M-305 S2, the
// task-level `checkin` stamp any run can write.
func TestHasWakeupCheckinSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  string
		want bool
	}{
		{name: "task checkin", ctx: `{"checkin":{"note":"no update"}}`, want: true},
		{name: "task checkin null", ctx: `{"checkin":null}`, want: false},
		{name: "wakeup checkin", ctx: `{"wakeup_id":"w1","wakeup_checkin":{"note":"nothing new"}}`, want: true},
		{name: "wakeup checkin null", ctx: `{"wakeup_id":"w1","wakeup_checkin":null}`, want: false},
		// The legacy wakeup marker alone keeps its wakeup-only semantics: a run
		// with the marker but no wakeup_id did NOT check in.
		{name: "wakeup marker without wakeup id", ctx: `{"wakeup_checkin":{"note":"x"}}`, want: false},
		{name: "empty context", ctx: `{}`, want: false},
		{name: "undecodable context", ctx: `not json`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := db.AgentTaskQueue{Context: json.RawMessage(tc.ctx)}
			if got := HasWakeupCheckin(task); got != tc.want {
				t.Fatalf("HasWakeupCheckin(%s) = %v, want %v", tc.ctx, got, tc.want)
			}
		})
	}
}

// The check-in marker is what makes the completion fallback (task.go) skip
// the synthesized root comment for a checked-in run.
func TestCompleteTaskSkipsFallbackOnTaskLevelCheckin(t *testing.T) {
	f, s, issue, agent := conditionFixture(t)
	ctx := context.Background()
	f.Cleanup(t, "DELETE FROM comment WHERE issue_id=$1", issue)
	output, _ := json.Marshal(protocol.TaskCompletedPayload{Output: "Checked; nothing new."})
	complete := func(taskCtx string) int {
		taskID := parseTestUUID(t, f.Task(t, agent, testutil.Cols{
			"issue_id":   issue,
			"status":     "running",
			"started_at": testutil.Raw("now()"),
			"context":    taskCtx,
			"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + agent + "')"),
		}))
		if _, err := s.Tasks.CompleteTask(ctx, taskID, output, "", "", "", false, "", ""); err != nil {
			t.Fatal(err)
		}
		return f.Count(t, "SELECT count(*) FROM comment WHERE issue_id=$1 AND author_type='agent' AND source_task_id=$2", issue, taskID)
	}
	if n := complete(`{"checkin":{"note":"no change since the last post"}}`); n != 0 {
		t.Fatalf("task-level checked-in run posted %d fallback comments, want 0", n)
	}
	// A run with the legacy marker but no wakeup_id did not check in: fall back.
	if n := complete(`{"wakeup_checkin":{"note":"nothing new"}}`); n != 1 {
		t.Fatalf("marker-only run posted %d fallback comments, want 1", n)
	}
	if n := complete(`{}`); n != 1 {
		t.Fatalf("plain run posted %d fallback comments, want 1", n)
	}
}
