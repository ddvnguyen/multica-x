package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	memoryIssueID = "11111111-1111-4111-8111-111111111111"
	memoryTaskID  = "22222222-2222-4222-8222-222222222222"
)

// setMemoryCmdFlag exercises the real command's registered flags: a missing
// --output or --since registration fails the test instead of silently passing
// a locally rebuilt flag set.
func setMemoryCmdFlag(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("%s has no --%s flag", cmd.Name(), name)
	}
	oldValue, oldChanged := flag.Value.String(), flag.Changed
	if err := flag.Value.Set(value); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
	flag.Changed = true
	t.Cleanup(func() {
		_ = flag.Value.Set(oldValue)
		flag.Changed = oldChanged
	})
}

func useMemoryTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
}

// The CLI reads a shadowed run's measurements and its saved mark, but never
// the two memory bodies: an agent rating the run must not be handed a second
// copy of its prompt context.
func TestIssueMemoryReportPrintsMeasurementsWithoutBlockText(t *testing.T) {
	const injectedBody = "INJECTED-MEMORY-BODY-MUST-NOT-PRINT"
	const shadowBody = "SHADOW-MEMORY-BODY-MUST-NOT-PRINT"
	var gotPath string
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"` + memoryTaskID + `","workspace_id":"workspace-1",
			"memory":{"part":"memory","backend":"moct","block":"` + injectedBody + `",
			  "bytes":900,"tokens":225,"latency_ms":40,
			  "entries":[{"id":"r1","title":"secret record","body":"ENTRY-BODY-MUST-NOT-PRINT"}],
			  "shadow":{"backend":"powercontext","block":"` + shadowBody + `","bytes":1200,"tokens":300,"latency_ms":150}},
			"mark":{"task_id":"` + memoryTaskID + `","choice":"powercontext","marked_by_type":"member",
			  "primary_bytes":900,"primary_latency_ms":40,"shadow_bytes":1200,"shadow_latency_ms":150,
			  "marked_at":"2026-11-27T19:00:00Z"}}`))
	})
	setMemoryCmdFlag(t, issueMemoryReportCmd, "output", "json")

	out, err := captureStdout(t, func() error {
		return runIssueMemoryReport(issueMemoryReportCmd, []string{memoryIssueID, memoryTaskID})
	})
	if err != nil {
		t.Fatalf("runIssueMemoryReport: %v", err)
	}
	wantPath := "/api/issues/" + memoryIssueID + "/tasks/" + memoryTaskID + "/memory-report"
	if gotPath != wantPath {
		t.Fatalf("GET path = %q, want %q", gotPath, wantPath)
	}
	for _, body := range []string{injectedBody, shadowBody, "ENTRY-BODY-MUST-NOT-PRINT", "secret record"} {
		if strings.Contains(out, body) {
			t.Fatalf("CLI output leaked memory body %q: %s", body, out)
		}
	}
	var view memoryReportView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", out, err)
	}
	if view.Injected.Backend != "moct" || view.Injected.Status != "block" || view.Injected.Bytes != 900 ||
		view.Injected.Tokens != 225 || view.Injected.LatencyMS != 40 {
		t.Fatalf("injected view = %+v", view.Injected)
	}
	if view.Shadow == nil || view.Shadow.Backend != "powercontext" || view.Shadow.Status != "block" ||
		view.Shadow.Bytes != 1200 || view.Shadow.Tokens != 300 || view.Shadow.LatencyMS != 150 {
		t.Fatalf("shadow view = %+v", view.Shadow)
	}
	if view.Mark == nil || view.Mark.Choice != "powercontext" || view.Mark.MarkedByType != "member" ||
		view.Mark.MarkedAt != "2026-11-27T19:00:00Z" {
		t.Fatalf("mark view = %+v", view.Mark)
	}
}

// A run with no report, an error note or a note-only shadow still reads as a
// status rather than as a block, so a reader never mistakes an empty shadow
// for one that was actually compared.
func TestMemoryBlockViewStatus(t *testing.T) {
	tests := []struct {
		name string
		part *memoryPartResponse
		want string
	}{
		{name: "no report", part: nil, want: "absent"},
		{name: "block", part: &memoryPartResponse{HasBlock: true}, want: "block"},
		{name: "error wins over note", part: &memoryPartResponse{Error: "timed out", Note: "note"}, want: "error"},
		{name: "note", part: &memoryPartResponse{Note: "skipped"}, want: "note"},
		{name: "no fields", part: &memoryPartResponse{}, want: "empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := memoryBlockViewOf(tc.part).Status; got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIssueMemoryReportTableLabelsUnmarkedRun(t *testing.T) {
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"` + memoryTaskID + `","workspace_id":"workspace-1",
			"memory":{"part":"memory","backend":"moct","shadow":{"backend":"powercontext","error":"timed out"}}}`))
	})
	setMemoryCmdFlag(t, issueMemoryReportCmd, "output", "table")

	out, err := captureStdout(t, func() error {
		return runIssueMemoryReport(issueMemoryReportCmd, []string{memoryIssueID, memoryTaskID})
	})
	if err != nil {
		t.Fatalf("runIssueMemoryReport: %v", err)
	}
	for _, want := range []string{"injected", "shadow", "error", "timed out", "mark: none"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output %q is missing %q", out, want)
		}
	}
}

func TestIssueMemoryMarkPostsTheChoice(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task_id":"` + memoryTaskID + `","choice":"neither","marked_by_type":"agent",
			"primary_bytes":900,"primary_latency_ms":40,"shadow_bytes":1200,"shadow_latency_ms":150,
			"marked_at":"2026-11-28T19:00:00Z"}`))
	})
	setMemoryCmdFlag(t, issueMemoryMarkCmd, "choice", "neither")
	setMemoryCmdFlag(t, issueMemoryMarkCmd, "output", "json")

	out, err := captureStdout(t, func() error {
		return runIssueMemoryMark(issueMemoryMarkCmd, []string{memoryIssueID, memoryTaskID})
	})
	if err != nil {
		t.Fatalf("runIssueMemoryMark: %v", err)
	}
	wantPath := "/api/issues/" + memoryIssueID + "/tasks/" + memoryTaskID + "/memory-mark"
	if gotPath != wantPath || gotMethod != http.MethodPost {
		t.Fatalf("request = %s %q, want POST %q", gotMethod, gotPath, wantPath)
	}
	if gotBody["choice"] != "neither" || len(gotBody) != 1 {
		t.Fatalf("POST body = %+v, want only choice=neither", gotBody)
	}
	var mark memoryMarkView
	if err := json.Unmarshal([]byte(out), &mark); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", out, err)
	}
	if mark.Choice != "neither" || mark.MarkedByType != "agent" || mark.ShadowBytes != 1200 {
		t.Fatalf("mark = %+v", mark)
	}
}

func TestIssueMemoryMarkRejectsAnInvalidChoiceWithoutPosting(t *testing.T) {
	posted := false
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		posted = true
	})
	setMemoryCmdFlag(t, issueMemoryMarkCmd, "choice", "moct-be")

	_, err := captureStdout(t, func() error {
		return runIssueMemoryMark(issueMemoryMarkCmd, []string{memoryIssueID, memoryTaskID})
	})
	if err == nil || !strings.Contains(err.Error(), "choice must be moct, powercontext or neither") {
		t.Fatalf("error = %v, want the server's choice vocabulary", err)
	}
	if posted {
		t.Fatal("invalid choice posted a vote; every mark counts in the M-613 tally")
	}
}
