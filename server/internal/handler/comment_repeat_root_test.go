package handler

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// repeatRootSeq keeps long-lived agent names unique inside the shared test
// workspace.
var repeatRootSeq atomic.Int64

// repeatRootFixture: an agent that already posted a top-level comment on the
// issue surface during a PREVIOUS run (different source_task_id), with no
// trigger comment anywhere — exactly the state the M-305 S2 repeat-root guard
// targets.
func repeatRootFixture(t *testing.T) (agentID, issueID, currentTaskID, rootID string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "repeat-root-r", testutil.Cols{"provider": "claude"})
	agentID = dbfx.Agent(t, fmt.Sprintf("Repeat root %d", repeatRootSeq.Add(1)), runtimeID)
	issueID = dbfx.Issue(t, "repeat root guard")
	currentTaskID = dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	// The stateroot of the earlier run: a completed task of the same agent.
	priorTaskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":     issueID,
		"runtime_id":   runtimeID,
		"status":       "completed",
		"started_at":   testutil.Raw("now() - interval '1 minute'"),
		"completed_at": testutil.Raw("now() - interval '1 second'"),
	})
	rootID = dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id":       issueID,
		"workspace_id":   testWorkspaceID,
		"author_type":    "agent",
		"author_id":      agentID,
		"content":        "shipping status: no change since the last post",
		"type":           "comment",
		"source_task_id": priorTaskID,
	})
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	return agentID, issueID, currentTaskID, rootID
}

func postRepeatRoot(t *testing.T, issueID, agentID, taskID string) *testutil.Response {
	t.Helper()
	req := agentRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "shipping status: no change since the last post",
	}, agentID, taskID)
	return testutil.Call(t, testHandler.CreateComment, withURLParam(req, "id", issueID))
}

// The guard redirects a repeat root (previous run's agent root still the
// newest comment, no other activity) to a thread reply or the check-in
// command. It never drops the comment silently — a 409 is returned.
func TestCreateComment_GuardsRepeatAgentRoot(t *testing.T) {
	agentID, issueID, currentTaskID, rootID := repeatRootFixture(t)
	resp := postRepeatRoot(t, issueID, agentID, currentTaskID)
	if resp.Code != http.StatusConflict {
		t.Fatalf("repeat root: expected 409, got %d: %s", resp.Code, resp.Body.String())
	}
	want := "reply under " + rootID + " or use `multica issue checkin`"
	if !strings.Contains(resp.Body.String(), want) {
		t.Fatalf("repeat root 409 body %q, want guidance %q", resp.Body.String(), want)
	}
}

// A newer comment (a reply from anyone) means there is activity the agent has
// not seen; opening a new root is then allowed.
func TestCreateComment_RepeatRootGuardPassesAfterOtherActivity(t *testing.T) {
	agentID, issueID, currentTaskID, _ := repeatRootFixture(t)
	dbfx.Comment(t, issueID, "newer reply from a teammate: please review")
	resp := postRepeatRoot(t, issueID, agentID, currentTaskID)
	if resp.Code != http.StatusCreated {
		t.Fatalf("root after other activity: expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
}

// The agent's own current run commenting again (e.g. progressive updates)
// must never be guarded: the blocking root came from this run.
func TestCreateComment_RepeatRootGuardPassesForSameRun(t *testing.T) {
	agentID, issueID, currentTaskID, rootID := repeatRootFixture(t)
	dbfx.Exec(t, `UPDATE comment SET source_task_id = $2 WHERE id = $1`, rootID, currentTaskID)
	resp := postRepeatRoot(t, issueID, agentID, currentTaskID)
	if resp.Code != http.StatusCreated {
		t.Fatalf("same-run root: expected 201, got %d: %s", resp.Code, resp.Body.String())
	}
}

// Comment-triggered runs keep their existing top-level rejection: the guard
// sits behind that path and never supersedes it.
func TestCreateComment_CommentTriggeredRootStillBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "triggered-root-r", testutil.Cols{"provider": "claude"})
	agentID := dbfx.Agent(t, fmt.Sprintf("Triggered root %d", repeatRootSeq.Add(1)), runtimeID)
	issueID := dbfx.Issue(t, "triggered root guard")
	triggerID := dbfx.Comment(t, issueID, "fix the failing test please")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":           issueID,
		"runtime_id":         runtimeID,
		"status":             "running",
		"started_at":         testutil.Raw("now()"),
		"trigger_comment_id": triggerID,
	})
	dbfx.Cleanup(t, `DELETE FROM comment WHERE issue_id = $1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	resp := postRepeatRoot(t, issueID, agentID, taskID)
	if resp.Code != http.StatusConflict {
		t.Fatalf("comment-triggered root: expected 409, got %d: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "comment-triggered tasks cannot create top-level comments") {
		t.Fatalf("comment-triggered 409 body %q changed with the repeat-root guard", resp.Body.String())
	}
}

// CheckInIssue stamps the task-level marker and leaves the timeline entry
// that keeps a silent run visible.
func TestCheckInIssueStampsTaskMarkerAndTimeline(t *testing.T) {
	agentID, issueID, currentTaskID, _ := repeatRootFixture(t)
	note := "no change since the last post"
	req := agentRequest(http.MethodPost, "/api/issues/"+issueID+"/checkin", map[string]any{"note": note}, agentID, currentTaskID)
	testutil.Call(t, testHandler.CheckInIssue, withURLParam(req, "id", issueID)).Want(http.StatusNoContent)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND context->'checkin'->>'note'=$2`, currentTaskID, note); n != 1 {
		t.Fatalf("task check-in marker = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='wakeup_checkin' AND details->>'note'=$2`, issueID, note); n != 1 {
		t.Fatalf("check-in timeline entries = %d, want 1", n)
	}
}
