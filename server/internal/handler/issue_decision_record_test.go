package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCreateCommentRecordsAndSupersedesOwnerDecision(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issueID := createCommentTriggerPreviewIssue(t, "decision record", "", "")

	post := func(body map[string]any) CommentResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
		testHandler.CreateComment(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateComment: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var response CommentResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode comment: %v", err)
		}
		return response
	}

	first := post(map[string]any{
		"content":            "Keep the deployment window.\nDo not deploy during business hours.",
		"record_as_decision": true,
	})
	var firstID string
	var decided, rationale, sourceKind string
	if err := testPool.QueryRow(ctx, `SELECT id, decided, rationale, source_kind FROM issue_decision_record WHERE comment_id=$1`, first.ID).Scan(&firstID, &decided, &rationale, &sourceKind); err != nil {
		t.Fatalf("read first decision record: %v", err)
	}
	if decided != "Keep the deployment window." || rationale != "Do not deploy during business hours." || sourceKind != "comment" {
		t.Fatalf("decision record = (%q, %q, %q), want parsed comment decision", decided, rationale, sourceKind)
	}

	second := post(map[string]any{
		"content":              "Deploy after business hours.\nSchedule the rollout for the evening.",
		"record_as_decision":   true,
		"supersedes_record_id": firstID,
	})

	var supersededBy string
	if err := testPool.QueryRow(ctx, `SELECT replacement.id FROM issue_decision_record old JOIN issue_decision_record replacement ON replacement.supersedes_id=old.id WHERE old.id=$1`, firstID).Scan(&supersededBy); err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if supersededBy == "" {
		t.Fatal("old decision was not marked superseded")
	}

	readReq := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/decision-records", nil), "issueId", issueID)
	testutil.Call(t, testHandler.ListIssueDecisionRecords, readReq).Want(http.StatusOK)
	var total int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue_decision_record WHERE issue_id=$1`, issueID).Scan(&total); err != nil {
		t.Fatalf("count decision records: %v", err)
	}
	if total != 2 || second.ID == "" {
		t.Fatalf("decision count = %d, want two records", total)
	}
}

func TestCreateCommentDecisionFlagRequiresHumanActor(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createCommentTriggerPreviewIssue(t, "agent decision record denied", "", "")
	agentID := createHandlerTestAgent(t, "Decision Flag Agent", nil)
	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id)
		VALUES ($1, $2, 'completed', 0, $3) RETURNING id
	`, agentID, handlerTestRuntimeID(t), issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed agent task: %v", err)
	}
	request := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
		"content": "Agent-authored decision", "record_as_decision": true,
	}), "id", issueID)
	request.Header.Set("X-Agent-ID", agentID)
	request.Header.Set("X-Task-ID", taskID)
	w := httptest.NewRecorder()
	testHandler.CreateComment(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("CreateComment with agent decision flag = %d, want 403: %s", w.Code, w.Body.String())
	}
}
