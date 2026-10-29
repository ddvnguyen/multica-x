package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dshsession"
)

// seedDshTask builds a running, issue-backed task on a dsh runtime — the shape
// M-102's sink and transcript path branch on.
func seedDshTask(t *testing.T, label string) string {
	t.Helper()
	dshRuntimeID := dbfx.Runtime(t, label+" dsh runtime", testutil.Cols{
		"provider":     "dsh",
		"runtime_mode": "local",
		"device_info":  "dsh sink test",
	})
	agentID := dbfx.Agent(t, label+" agent", dshRuntimeID, testutil.Cols{
		"instructions": "",
		"custom_env":   testutil.Raw("'{}'::jsonb"),
		"custom_args":  testutil.Raw("'[]'::jsonb"),
	})
	issueID := dbfx.Issue(t, label+" fixture", testutil.Cols{"status": "in_progress"})
	return dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": dshRuntimeID,
		"issue_id":   issueID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
		"session_id": "multica-00000000-0000-7000-8000-00000000000a",
		"work_dir":   "/task/workdir",
	})
}

// userMessagesRequest builds the user-authenticated GET the transcript dialog
// sends, with the workspace injected the way the router's middleware does.
func userMessagesRequest(t *testing.T, taskID string) *http.Request {
	t.Helper()
	req := testutil.JSONRequest(http.MethodGet, "/api/tasks/"+taskID+"/messages", nil)
	req = testutil.WithURLParams(req, "taskId", taskID)
	memberRow, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      util.MustParseUUID(testUserID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load test member row: %v", err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, memberRow))
}

// TestReportTaskMessagesDshSinkKeepsQuestionsOnly pins the M-102 drain-path
// gate: a dsh-runtime task's transcript frames become no rows and no events,
// while its question rows (the ask/plan control plane) persist and publish.
func TestReportTaskMessagesDshSinkKeepsQuestionsOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	taskID := seedDshTask(t, "dsh-sink")

	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, []any{
		map[string]any{"seq": 1, "type": "text", "content": "working on it"},
		map[string]any{"seq": 2, "type": "tool_use", "tool": "bash", "input": map[string]any{"command": "ls"}},
		map[string]any{"seq": 3, "type": "question", "content": "Which plan?", "input": map[string]any{
			"questions": []any{map[string]any{"question": "Approve this plan?"}},
		}},
	})).Want(http.StatusOK)

	stored, err := testHandler.Queries.ListTaskMessages(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("list task messages: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("dsh sink persisted %d rows, want only the question", len(stored))
	}
	if stored[0].Type != "question" || stored[0].Seq != 3 {
		t.Fatalf("persisted row = type %q seq %d, want the question at seq 3", stored[0].Type, stored[0].Seq)
	}
}

// TestReportTaskMessagesNonDshPathUnchanged pins that a non-dsh runtime still
// persists its whole batch — the sink must never leak onto other runtimes.
func TestReportTaskMessagesNonDshPathUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	taskID := seedBatchTask(t, "non-dsh-sink")

	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, []any{
		map[string]any{"seq": 1, "type": "text", "content": "kept"},
	})).Want(http.StatusOK)

	if n := dbfx.Count(t, `SELECT COUNT(*) FROM task_message WHERE task_id = $1`, taskID); n != 1 {
		t.Fatalf("task_message rows = %d, want 1", n)
	}
}

// TestListTaskMessagesByUserDshTranscript covers the read side: a dsh-owned
// task renders its transcript from the dsh session store, with stored rows
// (its questions) ahead of the session stream.
func TestListTaskMessagesByUserDshTranscript(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	taskID := seedDshTask(t, "dsh-read")

	// One stored question row — the only thing a sunk task writes.
	testutil.Call(t, testHandler.ReportTaskMessages, batchMessagesRequest(t, taskID, []any{
		map[string]any{"seq": 57, "type": "question", "content": "Pick a lane"},
	})).Want(http.StatusOK)
	if n := dbfx.Count(t, `SELECT COUNT(*) FROM task_message WHERE task_id = $1`, taskID); n != 1 {
		t.Fatalf("question rows = %d, want 1", n)
	}

	// A dsh session in a temp store, laid out the way the runtime bridge does:
	// projectKey(cwd) / session id / session.v4.jsonl.zstd.
	const sessionID = "multica-00000000-0000-7000-8000-00000000000a"
	store := t.TempDir()
	dir := filepath.Join(store, dshsession.ProjectKey("/task/workdir"), sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"session","version":4,"id":"` + sessionID + `","createdAt":1,"cwd":"/task/workdir","isSeeded":false,"delegationDepth":0}`,
		`{"type":"assistant/message","seq":18,"time":1000,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"hello from dsh"}]}}}`,
		`{"type":"tool/call","seq":19,"time":1001,"data":{"turn":1,"step":1,"callId":"call_1","name":"bash","arguments":"{\"command\":\"echo hi\"}"}}`,
		`{"type":"tool/result","seq":22,"time":1002,"data":{"turn":1,"step":1,"message":{"role":"tool","toolCallId":"call_1","isError":false,"content":[{"type":"text","text":"hi"}]}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl.zstd"), zstdCompress(t, log), 0o644); err != nil {
		t.Fatal(err)
	}

	testHandler.dshSessionStore = store
	t.Cleanup(func() { testHandler.dshSessionStore = "" })

	rec := testutil.Call(t, testHandler.ListTaskMessagesByUser, userMessagesRequest(t, taskID)).Want(http.StatusOK)
	var payloads []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payloads); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payloads) != 4 {
		t.Fatalf("got %d transcript entries, want question + 3 session entries: %+v", len(payloads), payloads)
	}
	if payloads[0]["type"] != "question" || payloads[0]["seq"] != float64(57) {
		t.Fatalf("first entry = %+v, want the stored question row", payloads[0])
	}
	if payloads[1]["type"] != "text" || payloads[1]["content"] != "hello from dsh" {
		t.Fatalf("second entry = %+v, want the dsh assistant text", payloads[1])
	}
	if payloads[2]["type"] != "tool_use" || payloads[2]["tool"] != "bash" {
		t.Fatalf("third entry = %+v, want the dsh tool_use", payloads[2])
	}
	if payloads[3]["type"] != "tool_result" || payloads[3]["output"] != "hi" {
		t.Fatalf("fourth entry = %+v, want the dsh tool_result", payloads[3])
	}
	// Session entries anchor after the highest stored row seq, keeping the
	// stream append-only across reads.
	if payloads[1]["seq"] != float64(58) {
		t.Fatalf("session entry seq = %v, want 58 (anchored after the question row)", payloads[1]["seq"])
	}
}

// With the store unset the endpoint must fall back to the plain rows — the
// pre-M-102 behavior, and the degradation path for any unreadable session.
func TestListTaskMessagesByUserDshFallbackToRows(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	taskID := seedDshTask(t, "dsh-fallback")

	testHandler.dshSessionStore = ""
	t.Cleanup(func() { testHandler.dshSessionStore = "" })

	rec := testutil.Call(t, testHandler.ListTaskMessagesByUser, userMessagesRequest(t, taskID)).Want(http.StatusOK)
	var payloads []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payloads); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// No rows and no readable store: an empty transcript, not an error.
	if len(payloads) != 0 {
		t.Fatalf("got %d entries, want 0 on fallback", len(payloads))
	}
}

// zstdCompress produces the on-disk encoding dsh writes for its session logs.
func zstdCompress(t *testing.T, data string) []byte {
	t.Helper()
	var buf strings.Builder
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buf.String())
}
