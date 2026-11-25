package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func TestIssueDecisionsJSONUsesCurrentEndpoint(t *testing.T) {
	const issueID = "11111111-1111-4111-8111-111111111111"
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		if r.Method != http.MethodGet {
			t.Errorf("request method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"record-1","decided":"Use the shared query","rationale":"Avoid drift"}]`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-1")
	t.Setenv("MULTICA_TOKEN", "test-token")

	cmd := &cobra.Command{Use: "decisions"}
	cmd.Flags().String("output", "table", "")
	if err := cmd.Flags().Set("output", "json"); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return runIssueDecisions(cmd, []string{issueID}) })
	if err != nil {
		t.Fatalf("runIssueDecisions: %v", err)
	}
	if gotPath != "/api/issues/"+issueID+"/decision-records" || gotQuery != "current=true" {
		t.Fatalf("request = %s?%s, want current decision endpoint", gotPath, gotQuery)
	}
	var records []issueDecision
	if err := json.Unmarshal([]byte(out), &records); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", out, err)
	}
	if len(records) != 1 || records[0].ID != "record-1" || records[0].Decided != "Use the shared query" || records[0].Rationale != "Avoid drift" {
		t.Fatalf("CLI JSON records = %+v, want returned id, decided, and rationale", records)
	}
}
