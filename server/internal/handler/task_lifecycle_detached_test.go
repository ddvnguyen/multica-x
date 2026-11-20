package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeHasDetachedCapability(t *testing.T) {
	meta, _ := json.Marshal(map[string]any{
		"capabilities": []string{protocol.DaemonCapabilityDetachedRuntimeV1},
	})
	rt := db.AgentRuntime{Metadata: meta}
	if !runtimeHasDetachedCapability(rt) {
		t.Fatal("expected detached capability to be detected")
	}

	meta2, _ := json.Marshal(map[string]any{
		"capabilities": []string{"other-cap"},
	})
	rt2 := db.AgentRuntime{Metadata: meta2}
	if runtimeHasDetachedCapability(rt2) {
		t.Fatal("unexpected detached capability")
	}

	rt3 := db.AgentRuntime{}
	if runtimeHasDetachedCapability(rt3) {
		t.Fatal("empty metadata should not be detached")
	}
}

func recoverOrphansCall(t *testing.T, runtimeID string) *testutil.Response {
	t.Helper()
	req := withURLParam(newOwnerDaemonRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/recover-orphans", nil), "runtimeId", runtimeID)
	return testutil.Call(t, testHandler.RecoverOrphanedTasks, req)
}

func TestRecoverOrphanedTasks_DetachedReturnsRunningTasks(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	meta, _ := json.Marshal(map[string]any{
		"capabilities": []string{protocol.DaemonCapabilityDetachedRuntimeV1},
	})
	runtimeID := dbfx.Runtime(t, "recover orphans detached runtime", testutil.Cols{
		"metadata": testutil.Raw("'" + string(meta) + "'::jsonb"),
	})
	agentID := dbfx.Agent(t, "recover orphans detached agent", runtimeID)
	runningID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "running",
		"session_id": "sess-detached", "work_dir": "/tmp/detached-work",
		"started_at": testutil.Raw("now()"),
	})
	dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "completed",
		"session_id": "sess-done", "work_dir": "/tmp/done-work",
		"started_at": testutil.Raw("now() - interval '1 hour'"), "completed_at": testutil.Raw("now()"),
	})

	var out struct {
		Orphaned        int                   `json:"orphaned"`
		Retried         int                   `json:"retried"`
		Detached        bool                  `json:"detached"`
		RunningDetached []RunningDetachedTask `json:"running_detached"`
	}
	recoverOrphansCall(t, runtimeID).Want(http.StatusOK).JSON(&out)

	if !out.Detached || out.Orphaned != 0 || out.Retried != 0 {
		t.Fatalf("unexpected counters: %+v", out)
	}
	want := []RunningDetachedTask{{TaskID: runningID, WorkDir: "/tmp/detached-work", SessionID: "sess-detached"}}
	if len(out.RunningDetached) != 1 || out.RunningDetached[0] != want[0] {
		t.Fatalf("running_detached = %+v, want %+v", out.RunningDetached, want)
	}
	// Detached runs are never failed by recover-orphans.
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, runningID).Scan(&status)
	if status != "running" {
		t.Fatalf("running task status = %q, want running", status)
	}
}

func TestRecoverOrphanedTasks_DetachedWithoutRunningTasksOmitsList(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	meta, _ := json.Marshal(map[string]any{
		"capabilities": []string{protocol.DaemonCapabilityDetachedRuntimeV1},
	})
	runtimeID := dbfx.Runtime(t, "recover orphans detached idle runtime", testutil.Cols{
		"metadata": testutil.Raw("'" + string(meta) + "'::jsonb"),
	})
	m := recoverOrphansCall(t, runtimeID).Want(http.StatusOK).Map()
	if m["detached"] != true {
		t.Fatalf("detached = %v, want true", m["detached"])
	}
	if _, ok := m["running_detached"]; ok {
		t.Fatalf("running_detached must be omitted when empty: %v", m)
	}
}

func TestRecoverOrphanedTasks_NonDetachedUnchanged(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "recover orphans legacy runtime")
	agentID := dbfx.Agent(t, "recover orphans legacy agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "status": "running",
		"session_id": "sess-legacy", "work_dir": "/tmp/legacy-work",
		"started_at": testutil.Raw("now()"),
	})

	m := recoverOrphansCall(t, runtimeID).Want(http.StatusOK).Map()
	if _, ok := m["running_detached"]; ok {
		t.Fatalf("non-detached response must not carry running_detached: %v", m)
	}
	if _, ok := m["detached"]; ok {
		t.Fatalf("non-detached response must not carry detached: %v", m)
	}
	if m["orphaned"] != float64(1) {
		t.Fatalf("orphaned = %v, want 1", m["orphaned"])
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	if status != "failed" {
		t.Fatalf("legacy running task status = %q, want failed", status)
	}
}
