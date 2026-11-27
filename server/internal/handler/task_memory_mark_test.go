package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestMemoryNumbersOf(t *testing.T) {
	tests := []struct {
		name       string
		part       string
		wantBytes  int
		wantShadow bool
	}{
		{name: "no part", part: "", wantShadow: false},
		{name: "part from a daemon that records neither", part: `{"part":"memory","tokens":120,"note":"injected"}`, wantShadow: false},
		{
			name:       "injected and shadow blocks measured",
			part:       `{"part":"memory","backend":"moct","bytes":900,"latency_ms":40,"shadow":{"backend":"powercontext","bytes":1200,"latency_ms":150}}`,
			wantBytes:  900,
			wantShadow: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			numbers := memoryNumbersOf(json.RawMessage(tc.part))
			if numbers.bytes != tc.wantBytes {
				t.Fatalf("bytes = %d, want %d", numbers.bytes, tc.wantBytes)
			}
			if numbers.hasShadow != tc.wantShadow {
				t.Fatalf("hasShadow = %v, want %v", numbers.hasShadow, tc.wantShadow)
			}
			if !tc.wantShadow {
				return
			}
			if numbers.backend != "moct" || numbers.shadowBackend != "powercontext" {
				t.Fatalf("backends = %q / %q", numbers.backend, numbers.shadowBackend)
			}
			if numbers.bytes != 900 || numbers.latencyMS != 40 || numbers.shadowBytes != 1200 || numbers.shadowLatencyMS != 150 {
				t.Fatalf("measurements = %+v", numbers)
			}
		})
	}
}

// memoryCompareFixture builds a run whose memory part recorded both blocks.
func memoryCompareFixture(t *testing.T, workspaceID string, part string) (issueID, taskID string) {
	t.Helper()
	runtimeID := dbfx.Runtime(t, "compare-runtime", testutil.Cols{"workspace_id": workspaceID})
	agentID := dbfx.Agent(t, "compare-agent", runtimeID, testutil.Cols{"workspace_id": workspaceID})
	issueID = dbfx.Issue(t, "Compare memory", testutil.Cols{"workspace_id": workspaceID})
	// agent_task_queue carries no workspace_id: the tenant guard joins the
	// owning agent, which the fixture already seats in workspaceID. A queued
	// row also has to name its runtime (active_requires_runtime).
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"result":     testutil.Raw(`'{"init_context":{"parts":[` + part + `]}}'::jsonb`),
	})
	return issueID, taskID
}

func callMemoryMark(t *testing.T, handler http.HandlerFunc, method, workspaceID, issueID, taskID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := testutil.WithHeaders(httptest.NewRequest(method, "/", strings.NewReader(body)),
		"X-User-ID", testUserID, "X-Workspace-ID", workspaceID)
	req.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("id", issueID)
	route.URLParams.Add("taskId", taskID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

// A run with only one block cannot be compared: letting it vote would count a
// guess as evidence in the workspace's preference share.
func TestSetTaskMemoryMarkRejectsARunWithoutAShadow(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Memory compare single", "memory-compare-single-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	issueID, taskID := memoryCompareFixture(t, workspaceID,
		`{"part":"memory","backend":"moct","bytes":900,"latency_ms":40}`)

	recorder := callMemoryMark(t, testHandler.SetTaskMemoryMark, http.MethodPost, workspaceID, issueID, taskID, `{"choice":"moct"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
}

func TestSetTaskMemoryMarkRejectsAnUnknownChoice(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Memory compare choice", "memory-compare-choice-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	issueID, taskID := memoryCompareFixture(t, workspaceID,
		`{"part":"memory","backend":"moct","bytes":900,"shadow":{"backend":"powercontext","bytes":1200}}`)

	recorder := callMemoryMark(t, testHandler.SetTaskMemoryMark, http.MethodPost, workspaceID, issueID, taskID, `{"choice":"moct-be"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
}

// The vote records what the run measured, re-voting replaces it, and the
// workspace aggregate counts runs rather than clicks.
func TestSetTaskMemoryMarkRecordsTheRunsNumbers(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Memory compare votes", "memory-compare-votes-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	issueID, taskID := memoryCompareFixture(t, workspaceID,
		`{"part":"memory","backend":"moct","bytes":900,"latency_ms":40,"shadow":{"backend":"powercontext","bytes":1200,"latency_ms":150}}`)

	recorder := callMemoryMark(t, testHandler.SetTaskMemoryMark, http.MethodPost, workspaceID, issueID, taskID, `{"choice":"powercontext"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var mark MemoryMarkResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &mark); err != nil {
		t.Fatalf("decode mark: %v", err)
	}
	if mark.Choice != "powercontext" || mark.MarkedByType != "member" {
		t.Fatalf("mark = %+v", mark)
	}
	if mark.PrimaryBytes != 900 || mark.PrimaryLatencyMS != 40 || mark.ShadowBytes != 1200 || mark.ShadowLatencyMS != 150 {
		t.Fatalf("mark numbers = %+v, want the run's own measurements", mark)
	}

	// The report carries the vote back to the panel that made it.
	report := callMemoryMark(t, testHandler.GetTaskMemoryReport, http.MethodGet, workspaceID, issueID, taskID, "")
	if report.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", report.Code, report.Body.String())
	}
	var reportBody struct {
		Mark *MemoryMarkResponse `json:"mark"`
	}
	if err := json.Unmarshal(report.Body.Bytes(), &reportBody); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if reportBody.Mark == nil || reportBody.Mark.Choice != "powercontext" {
		t.Fatalf("report mark = %+v, want the saved vote", reportBody.Mark)
	}

	// A second vote replaces the first: the aggregate counts this run once.
	recorder = callMemoryMark(t, testHandler.SetTaskMemoryMark, http.MethodPost, workspaceID, issueID, taskID, `{"choice":"moct"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("re-vote status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// The workspace route carries the workspace id in its own {id} param.
	aggregate := callMemoryMark(t, inboxWorkspaceHandler(testHandler.GetWorkspaceMemoryComparison), http.MethodGet, workspaceID, workspaceID, "", "")
	if aggregate.Code != http.StatusOK {
		t.Fatalf("aggregate status=%d body=%s", aggregate.Code, aggregate.Body.String())
	}
	var comparison MemoryComparisonResponse
	if err := json.Unmarshal(aggregate.Body.Bytes(), &comparison); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if comparison.Marked != 1 || comparison.Votes.Moct != 1 || comparison.Votes.PowerContext != 0 {
		t.Fatalf("aggregate = %+v, want one moct vote", comparison)
	}
	if comparison.PreferencePct.Moct != 100 {
		t.Fatalf("preference = %+v, want 100%% moct", comparison.PreferencePct)
	}
	if len(comparison.Backends) != 2 {
		t.Fatalf("backends = %+v, want both planes", comparison.Backends)
	}
	byBackend := map[string]MemoryComparisonBackend{}
	for _, backend := range comparison.Backends {
		byBackend[backend.Backend] = backend
	}
	if got := byBackend["moct"]; got.Bytes != 900 || got.LatencyMS != 40 {
		t.Fatalf("moct median = %+v", got)
	}
	if got := byBackend["powercontext"]; got.Bytes != 1200 || got.LatencyMS != 150 {
		t.Fatalf("powercontext median = %+v", got)
	}
}

// An unmarked workspace is a zero row, not an error: the panel renders before
// anyone has voted.
func TestWorkspaceMemoryComparisonIsEmptyBeforeAnyVote(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Memory compare empty", "memory-compare-empty-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")

	// The aggregate over no rows is a zero row, not a NULL scan: assert the
	// query itself first so a scan failure reads as one.
	stats, err := testHandler.Queries.GetWorkspaceMemoryMarkStats(context.Background(), parseUUID(workspaceID))
	if err != nil {
		t.Fatalf("empty aggregate query: %v", err)
	}
	if stats.Marked != 0 {
		t.Fatalf("marked = %d, want 0", stats.Marked)
	}

	recorder := callMemoryMark(t, inboxWorkspaceHandler(testHandler.GetWorkspaceMemoryComparison), http.MethodGet, workspaceID, workspaceID, "", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var comparison MemoryComparisonResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &comparison); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if comparison.Marked != 0 || comparison.Votes.Moct != 0 || comparison.PreferencePct.Moct != 0 {
		t.Fatalf("aggregate = %+v, want an empty tally", comparison)
	}
}
