package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestIssueDecisionAddPostsProblemDecisionAndHumanComment(t *testing.T) {
	const issueID = "11111111-1111-4111-8111-111111111111"
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("request method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"record-1","problem":"Which policy?","decided":"Use the shared policy","comment_id":"human-comment","scope":"project"}`))
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := &cobra.Command{Use: "add"}
	cmd.Flags().String("problem", "Which policy?", "")
	cmd.Flags().String("decided", "Use the shared policy", "")
	cmd.Flags().String("comment", "human-comment", "")
	cmd.Flags().String("problem-comment", "question-comment", "")
	cmd.Flags().String("supersedes", "", "")
	cmd.Flags().String("scope", "project", "")
	cmd.Flags().String("output", "json", "")
	out, err := captureStdout(t, func() error { return runIssueDecisionAdd(cmd, []string{issueID}) })
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/issues/"+issueID+"/decision-records" {
		t.Fatalf("POST path = %q", gotPath)
	}
	if gotBody["problem"] != "Which policy?" || gotBody["decided"] != "Use the shared policy" || gotBody["comment_id"] != "human-comment" || gotBody["problem_comment_id"] != "question-comment" || gotBody["scope"] != "project" {
		t.Fatalf("POST body = %+v", gotBody)
	}
	if !strings.Contains(out, `"scope": "project"`) {
		t.Fatalf("JSON output = %q", out)
	}
}
