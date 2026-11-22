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

	readReq := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/decision-records", nil), "id", issueID)
	readResponse := httptest.NewRecorder()
	testHandler.ListIssueDecisionRecords(readResponse, readReq)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("ListIssueDecisionRecords = %d: %s", readResponse.Code, readResponse.Body.String())
	}
	var records []issueDecisionRecordResponse
	if err := json.NewDecoder(readResponse.Body).Decode(&records); err != nil {
		t.Fatalf("decode decision records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("listed %d decision records, want 2", len(records))
	}
	firstListed := records[0]
	secondListed := records[1]
	if firstListed.ID != firstID || firstListed.SupersededByID == nil || *firstListed.SupersededByID != secondListed.ID {
		t.Fatalf("listed old decision = %+v, replacement = %+v; want superseded_by_id to name replacement", firstListed, secondListed)
	}
	var total int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue_decision_record WHERE issue_id=$1`, issueID).Scan(&total); err != nil {
		t.Fatalf("count decision records: %v", err)
	}
	if total != 2 || second.ID == "" {
		t.Fatalf("decision count = %d, want two records", total)
	}
}

func TestCreateCommentRejectsInvalidSupersedesTargets(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createCommentTriggerPreviewIssue(t, "decision supersede target", "", "")
	otherIssueID := createCommentTriggerPreviewIssue(t, "foreign decision supersede target", "", "")

	post := func(issueID string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID)
		testHandler.CreateComment(w, req)
		return w
	}
	create := func(issueID, content string, supersedesID string) string {
		t.Helper()
		body := map[string]any{"content": content, "record_as_decision": true}
		if supersedesID != "" {
			body["supersedes_record_id"] = supersedesID
		}
		w := post(issueID, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("create decision: %d: %s", w.Code, w.Body.String())
		}
		var response CommentResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode comment: %v", err)
		}
		var recordID string
		if err := testPool.QueryRow(context.Background(), `SELECT id FROM issue_decision_record WHERE comment_id=$1`, response.ID).Scan(&recordID); err != nil {
			t.Fatalf("load decision record: %v", err)
		}
		return recordID
	}
	reject := func(issueID, targetID string) {
		t.Helper()
		w := post(issueID, map[string]any{
			"content": "Replacement decision", "record_as_decision": true, "supersedes_record_id": targetID,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("supersedes_record_id %q on issue %q = %d, want 400: %s", targetID, issueID, w.Code, w.Body.String())
		}
	}

	firstID := create(issueID, "First decision", "")
	foreignID := create(otherIssueID, "Foreign decision", "")
	_ = create(issueID, "First decision replacement", firstID)

	reject(issueID, "00000000-0000-7000-8000-000000000099")
	reject(issueID, foreignID)
	reject(issueID, firstID)
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
		t.Fatalf("count issue comments: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue_decision_record WHERE issue_id=$1`, issueID).Scan(&records); err != nil {
		t.Fatalf("count decision records: %v", err)
	}
	if comments != 0 || records != 0 {
		t.Fatalf("agent response wrote %d comments and %d records, want neither", comments, records)
	}
}

func jsonBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return bytes.NewReader(encoded)
}
