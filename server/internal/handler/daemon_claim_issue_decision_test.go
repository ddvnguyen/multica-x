package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCurrentIssueDecisionRecordsMatchEndpointAndClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Current decisions claim runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Current decisions claim agent")
	ids := seedIssueDecisionRecordsForCurrentRead(t, issueID)
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "120 seconds", false)

	readRequest := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/decision-records?current=true", nil), "id", issueID)
	readResponse := httptest.NewRecorder()
	testHandler.ListIssueDecisionRecords(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("ListIssueDecisionRecords current = %d: %s", readResponse.Code, readResponse.Body.String())
	}
	var endpointRecords []issueDecisionRecordResponse
	if err := json.Unmarshal(readResponse.Body.Bytes(), &endpointRecords); err != nil {
		t.Fatalf("decode current decisions: %v", err)
	}
	endpointIDs := decisionRecordIDs(endpointRecords)
	assertCurrentDecisionIDs(t, endpointIDs, ids.want)
	for _, id := range endpointIDs {
		if id == ids.old {
			t.Fatalf("current endpoint included superseded decision %s", ids.old)
		}
	}

	claimRequest := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil), "runtimeId", runtimeID)
	claimResponse := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(claimResponse, claimRequest)
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime = %d: %s", claimResponse.Code, claimResponse.Body.String())
	}
	var claim struct {
		Task *struct {
			ID              string                   `json:"id"`
			DecisionRecords []TaskDecisionRecordData `json:"decision_records"`
		} `json:"task"`
	}
	if err := json.Unmarshal(claimResponse.Body.Bytes(), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claim.Task == nil || claim.Task.ID != taskID {
		t.Fatalf("claim task = %+v, want task %s", claim.Task, taskID)
	}
	claimIDs := make([]string, 0, len(claim.Task.DecisionRecords))
	for _, record := range claim.Task.DecisionRecords {
		claimIDs = append(claimIDs, record.ID)
	}
	assertCurrentDecisionIDs(t, claimIDs, ids.want)
	for _, id := range claimIDs {
		if id == ids.old {
			t.Fatalf("claim included superseded decision %s", ids.old)
		}
	}
	if claim.Task.DecisionRecords[0].Decided != "decision 1" || claim.Task.DecisionRecords[len(claim.Task.DecisionRecords)-1].Decided != "replacement" {
		t.Fatalf("claim decision order = %+v, want oldest retained through newest", claim.Task.DecisionRecords)
	}
}

func TestClaimTaskByRuntime_ChatOmitsIssueDecisionRecords(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "Chat decision omission runtime")
	agentID := dbfx.Agent(t, "Chat decision omission agent", runtimeID)
	sessionID := dbfx.ChatSession(t, agentID, testutil.Cols{"creator_id": testUserID})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID, "priority": 1000, "chat_session_id": sessionID,
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
	})

	request := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil), "runtimeId", runtimeID)
	response := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime = %d: %s", response.Code, response.Body.String())
	}
	var claim struct {
		Task *map[string]any `json:"task"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &claim); err != nil {
		t.Fatalf("decode chat claim: %v", err)
	}
	if claim.Task == nil || (*claim.Task)["id"] != taskID {
		t.Fatalf("claim task = %+v, want chat task %s", claim.Task, taskID)
	}
	if _, present := (*claim.Task)["decision_records"]; present {
		t.Fatalf("chat claim carried issue decision records: %+v", (*claim.Task)["decision_records"])
	}
}

type seededDecisionIDs struct {
	old  string
	want []string
}

func seedIssueDecisionRecordsForCurrentRead(t *testing.T, issueID string) seededDecisionIDs {
	t.Helper()
	t.Cleanup(func() {
		if _, err := testPool.Exec(context.Background(), `DELETE FROM issue_decision_record WHERE issue_id = $1`, issueID); err != nil {
			t.Errorf("clean up issue decision records: %v", err)
		}
	})
	ids := make([]string, 11)
	for i := range ids {
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO issue_decision_record (
				id, workspace_id, issue_id, comment_id, decided, rationale, source_kind, actor_id, created_at
			) VALUES (
				gen_random_uuid(), $1, $2, gen_random_uuid(), $3, '', 'comment', $4,
				now() + ($5::int * interval '1 second')
			) RETURNING id
		`, testWorkspaceID, issueID, fmt.Sprintf("decision %d", i), testUserID, i).Scan(&ids[i]); err != nil {
			t.Fatalf("seed decision %d: %v", i, err)
		}
	}
	var replacementID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue_decision_record (
			id, workspace_id, issue_id, comment_id, decided, rationale, supersedes_id, source_kind, actor_id, created_at
		) VALUES (
			gen_random_uuid(), $1, $2, gen_random_uuid(), 'replacement', '', $3, 'comment', $4,
			now() + interval '11 seconds'
		) RETURNING id
	`, testWorkspaceID, issueID, ids[10], testUserID).Scan(&replacementID); err != nil {
		t.Fatalf("seed replacement decision: %v", err)
	}
	oldID := ids[10]
	want := append([]string(nil), ids[1:10]...)
	want = append(want, replacementID)
	return seededDecisionIDs{old: oldID, want: want}
}

func decisionRecordIDs(records []issueDecisionRecordResponse) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func assertCurrentDecisionIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != 10 {
		t.Fatalf("current decision count = %d, want 10: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("current decision IDs = %v, want chronological newest-ten set %v", got, want)
		}
	}
}
