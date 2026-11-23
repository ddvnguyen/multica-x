package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func postDecisionRecord(t *testing.T, issueID string, body map[string]any) (*httptest.ResponseRecorder, issueDecisionRecordResponse) {
	t.Helper()
	request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/decision-records", body), "id", issueID)
	w := httptest.NewRecorder()
	testHandler.CreateIssueDecisionRecord(w, request)
	var response issueDecisionRecordResponse
	if w.Code == http.StatusCreated || w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode decision record: %v", err)
		}
	}
	return w, response
}

func createDecisionSourceComment(t *testing.T, issueID, content string, agentID, taskID string) CommentResponse {
	t.Helper()
	request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID)
	if agentID != "" {
		request.Header.Set("X-Agent-ID", agentID)
	}
	if taskID != "" {
		request.Header.Set("X-Task-ID", taskID)
	}
	w := httptest.NewRecorder()
	testHandler.CreateComment(w, request)
	if w.Code != http.StatusCreated {
		t.Fatalf("create source comment: %d: %s", w.Code, w.Body.String())
	}
	var response CommentResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode source comment: %v", err)
	}
	return response
}

func TestAgentCanRecordDecisionFromMemberCommentAndRetryIdempotently(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issueID := createCommentTriggerPreviewIssue(t, "decision pairs", "", "")
	comment := createDecisionSourceComment(t, issueID, "Use the shared query implementation.", "", "")
	body := map[string]any{"problem": "Which query should we use?", "decided": "Use the shared query implementation.", "comment_id": comment.ID}
	w, first := postDecisionRecord(t, issueID, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create record = %d: %s", w.Code, w.Body.String())
	}
	if first.Problem != "Which query should we use?" || first.SourceKind != "agent_recorded" || first.Scope != "issue" || first.ActorID != comment.AuthorID {
		t.Fatalf("created record = %+v, want problem pair with human provenance", first)
	}
	w, retry := postDecisionRecord(t, issueID, body)
	if w.Code != http.StatusOK || retry.ID != first.ID {
		t.Fatalf("idempotent retry = %d, %+v; want 200 and %s", w.Code, retry, first.ID)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue_decision_record WHERE comment_id=$1`, comment.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("records for comment = %d, want 1", count)
	}
}

func TestDecisionRecordRejectsAgentAndOtherIssueComments(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createCommentTriggerPreviewIssue(t, "decision source", "", "")
	otherIssueID := createCommentTriggerPreviewIssue(t, "foreign decision source", "", "")
	foreign := createDecisionSourceComment(t, otherIssueID, "Use this decision.", "", "")
	w, _ := postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Use this decision.", "comment_id": foreign.ID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign issue comment = %d, want 400: %s", w.Code, w.Body.String())
	}
	agentID := createHandlerTestAgent(t, "Decision Source Agent", nil)
	var taskID string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id) VALUES ($1, $2, 'completed', 0, $3) RETURNING id`, agentID, handlerTestRuntimeID(t), issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed agent task: %v", err)
	}
	agentComment := createDecisionSourceComment(t, issueID, "Agent-authored comment.", agentID, taskID)
	w, _ = postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Agent-authored comment.", "comment_id": agentComment.ID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("agent comment = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestDecisionRecordSupersedesOnlyCurrentDecisionOnSameIssue(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createCommentTriggerPreviewIssue(t, "decision supersedes", "", "")
	otherIssueID := createCommentTriggerPreviewIssue(t, "foreign decision supersedes", "", "")
	firstComment := createDecisionSourceComment(t, issueID, "Initial resolution.", "", "")
	w, first := postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Initial resolution.", "comment_id": firstComment.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("create initial: %d: %s", w.Code, w.Body.String())
	}
	foreignComment := createDecisionSourceComment(t, otherIssueID, "Foreign resolution.", "", "")
	_, foreign := postDecisionRecord(t, otherIssueID, map[string]any{"problem": "Which?", "decided": "Foreign resolution.", "comment_id": foreignComment.ID})
	for _, target := range []string{"00000000-0000-7000-8000-000000000099", foreign.ID} {
		comment := createDecisionSourceComment(t, issueID, "Replacement resolution: "+target, "", "")
		w, _ := postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Replacement resolution: " + target, "comment_id": comment.ID, "supersedes_id": target})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid supersedes target %q = %d, want 400: %s", target, w.Code, w.Body.String())
		}
	}
	replacementComment := createDecisionSourceComment(t, issueID, "Replacement resolution.", "", "")
	w, replacement := postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Replacement resolution.", "comment_id": replacementComment.ID, "supersedes_id": first.ID})
	if w.Code != http.StatusCreated {
		t.Fatalf("create replacement: %d: %s", w.Code, w.Body.String())
	}
	w, _ = postDecisionRecord(t, issueID, map[string]any{"problem": "Which?", "decided": "Another resolution.", "comment_id": replacementComment.ID, "supersedes_id": first.ID})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supersede already replaced record = %d, want 400: %s", w.Code, w.Body.String())
	}
	var target string
	if err := testPool.QueryRow(context.Background(), `SELECT supersedes_id::text FROM issue_decision_record WHERE id=$1`, replacement.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != first.ID {
		t.Fatalf("replacement target = %q, want %q", target, first.ID)
	}
}

func TestDecisionResponseRouteRejectsAgentWithoutWritingCommentOrRecord(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, _, _, body := m209ReviewSetup(t)
	router := chi.NewRouter()
	router.With(RequireHumanActor).Post("/api/issues/{id}/decision-response", testHandler.DecisionResponse)
	request := httptest.NewRequest(http.MethodPost, "/api/issues/"+issueID+"/decision-response", jsonBody(t, body))
	request.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent decision-response = %d, want 403: %s", w.Code, w.Body.String())
	}
	var comments, records int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM comment WHERE issue_id=$1`, issueID).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue_decision_record WHERE issue_id=$1`, issueID).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if comments != 0 || records != 0 {
		t.Fatalf("agent response wrote %d comments and %d records, want neither", comments, records)
	}
}

func jsonBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return bytes.NewReader(encoded)
}
