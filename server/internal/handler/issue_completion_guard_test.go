package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Completion is a human decision: a machine-authored write may not put an
// issue into a done-category status.
//
// The tests below cover the two axes that decide the outcome — WHO wrote
// (machine credential vs human member) and WHAT status they wrote (done
// category vs anything else) — plus the paths a completion can arrive
// through: a single write, a batch write, and a create.

// agentAuthoredRequest returns a request that carries the server-stamped
// actor source of an agent run, the shape the auth middleware produces for a
// task token.
func agentAuthoredRequest(method, path string, body any) *http.Request {
	req := newRequest(method, path, body)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", uuid.NewString())
	return req
}

func issueStatusRow(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read issue status: %v", err)
	}
	return status
}

func TestIssueCompletionGuardRefusesAgentDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "completion guard agent done")

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "done"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent update to done: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body["code"] != "agent_cannot_complete_issue" {
		t.Fatalf("expected code agent_cannot_complete_issue, got %v", body["code"])
	}
	if got := issueStatusRow(t, issueID); got != "todo" {
		t.Fatalf("refused write must leave the status untouched, got %q", got)
	}
}

func TestIssueCompletionGuardAllowsAgentNonDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "completion guard agent review")

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "in_review"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent update to in_review: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusRow(t, issueID); got != "in_review" {
		t.Fatalf("expected the issue to be in_review, got %q", got)
	}
}

func TestIssueCompletionGuardAllowsHumanDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "completion guard human done")

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "done"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("human update to done: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusRow(t, issueID); got != "done" {
		t.Fatalf("expected the issue to be done, got %q", got)
	}
}

// A custom status in the done category is the same completion claim under
// another name, so the guard covers it too.
func TestIssueCompletionGuardCoversCustomDoneStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	key := "guarddone" + uuid.NewString()[:8]
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"key":          key,
		"name":         "Guard Done " + key,
		"category":     "done",
		"color":        "#000000",
	})
	issueID := dbfx.Issue(t, "completion guard custom done")

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": key})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent update to custom done status: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusRow(t, issueID); got != "todo" {
		t.Fatalf("refused write must leave the status untouched, got %q", got)
	}
}

// Cancelling is not a completion claim, so a machine may still cancel.
func TestIssueCompletionGuardAllowsAgentCancel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "completion guard agent cancel")

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "cancelled"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("agent update to cancelled: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusRow(t, issueID); got != "cancelled" {
		t.Fatalf("expected the issue to be cancelled, got %q", got)
	}
}

func TestIssueCompletionGuardRefusesAgentBatchDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "completion guard batch")

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("POST", "/api/issues/batch", map[string]any{
		"issue_ids": []string{issueID},
		"updates":   map[string]any{"status": "done"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent batch to done: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusRow(t, issueID); got != "todo" {
		t.Fatalf("refused write must leave the status untouched, got %q", got)
	}
}

func TestIssueCompletionGuardRefusesAgentCreateDone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	w := httptest.NewRecorder()
	req := agentAuthoredRequest("POST", "/api/issues", map[string]any{
		"title":  "completion guard create",
		"status": "done",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent create in done: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestIssueCompletionRefusedMatrix(t *testing.T) {
	cases := []struct {
		machine  bool
		category string
		want     bool
	}{
		{machine: true, category: "done", want: true},
		{machine: true, category: "started", want: false},
		{machine: true, category: "unstarted", want: false},
		{machine: true, category: "closed", want: false},
		{machine: false, category: "done", want: false},
	}
	for _, tc := range cases {
		if got := issueCompletionRefused(tc.machine, tc.category); got != tc.want {
			t.Fatalf("issueCompletionRefused(%v, %q) = %v, want %v", tc.machine, tc.category, got, tc.want)
		}
	}
}
