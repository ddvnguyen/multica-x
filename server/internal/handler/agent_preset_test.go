package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAgentPresetCRUDAndWorkspaceGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var previous []byte
	if err := testPool.QueryRow(context.Background(), `SELECT settings FROM workspace WHERE id=$1`, testWorkspaceID).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `UPDATE workspace SET settings=$1 WHERE id=$2`, previous, testWorkspaceID) })
	testPool.Exec(context.Background(), `UPDATE workspace SET settings=COALESCE(settings,'{}'::jsonb)||'{"agent_presets_enabled":true}'::jsonb WHERE id=$1`, testWorkspaceID)
	agentID, _, _, tierID, _ := tierAgentFixture(t)
	path := "/api/workspaces/" + testWorkspaceID + "/presets"
	create := func(name string) *httptest.ResponseRecorder {
		t.Helper()
		return testutil.Call(t, testHandler.CreateAgentPreset, withURLParam(newRequestAs(testUserID, http.MethodPost, path, map[string]any{
			"name": name, "agent_id": agentID, "tier_id": tierID,
		}), "id", testWorkspaceID)).ResponseRecorder
	}
	created := create("Architect Sonnet")
	if created.Code != http.StatusOK && created.Code != http.StatusCreated {
		t.Fatalf("create preset: status %d: %s", created.Code, created.Body.String())
	}
	var result struct { ID string `json:"id"`; WorkspaceID string `json:"workspace_id"`; Name string `json:"name"`; AgentID string `json:"agent_id"`; TierID string `json:"tier_id"` }
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil { t.Fatal(err) }
	if result.ID == "" || result.WorkspaceID != testWorkspaceID || result.AgentID != agentID || result.TierID != tierID {
		t.Fatalf("unexpected preset response: %+v", result)
	}
	if duplicate := create("architect sonnet"); duplicate.Code != http.StatusConflict {
		t.Fatalf("case-insensitive duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	listed := testutil.Call(t, testHandler.ListAgentPresets, withURLParam(newRequestAs(testUserID, http.MethodGet, path, nil), "id", testWorkspaceID)).Want(http.StatusOK).Text()
	if !strings.Contains(listed, `"name":"Architect Sonnet"`) { t.Fatalf("list omitted preset: %s", listed) }
	deleted := testutil.Call(t, testHandler.DeleteAgentPreset, withURLParams(newRequestAs(testUserID, http.MethodDelete, path+"/"+result.ID, nil), "id", testWorkspaceID, "presetId", result.ID))
	deleted.Want(http.StatusNoContent)
}
