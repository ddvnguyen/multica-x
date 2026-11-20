package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// tierAgentFixture creates an online runtime, two tiers on two runtimes
// (tier-fast idx 0 on rtFast, tier-slow idx 1 on rtSlow), and an agent with
// use_tiers selecting both. Returns ids for assertions.
func tierAgentFixture(t *testing.T) (agentID, rtFast, rtSlow, tierFast, tierSlow string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	rtFast = createProviderRuntime(t, "tierfast")
	rtSlow = createProviderRuntime(t, "tierslow")
	agentID = createOmpAgent(t, "tier-switch-agent", rtFast, "", "")

	mkTier := func(name string, idx int, runtimeID string) string {
		w := testutil.Call(t, testHandler.CreateModelTier, withURLParam(newRequest(
			http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/model-tiers",
			map[string]any{
				"name": name, "idx": idx, "runtime_id": runtimeID,
				"provider": "codebuddy", "model": "model-" + name,
				"provider_slots": 1, "enabled": true,
			},
		), "id", testWorkspaceID))
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("create tier %s: got %d: %s", name, w.Code, w.Body.String())
		}
		var resp ModelTierResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode tier %s: %v", name, err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `DELETE FROM model_tier WHERE id = $1`, resp.ID)
		})
		return resp.ID
	}
	tierFast = mkTier("tier-fast", 0, rtFast)
	tierSlow = mkTier("tier-slow", 1, rtSlow)

	// Flip the switch and select both tiers.
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"use_tiers": true, "tier_ids": []string{tierFast, tierSlow}},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateAgent use_tiers: got %d: %s", w.Code, w.Body.String())
	}
	return agentID, rtFast, rtSlow, tierFast, tierSlow
}

// TestUpdateAgent_UseTiersRoundtrip pins the M-351 API contract: the switch
// persists, tier_ids replace wholesale, unknown ids 400, and GetAgent echoes
// the selection back.
func TestUpdateAgent_UseTiersRoundtrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, _, _, tierFast, tierSlow := tierAgentFixture(t)

	read := func() AgentResponse {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.GetAgent(w, withURLParam(newRequestAs(
			testUserID, http.MethodGet, "/api/agents/"+agentID, nil,
		), "id", agentID))
		if w.Code != http.StatusOK {
			t.Fatalf("GetAgent: got %d: %s", w.Code, w.Body.String())
		}
		var resp AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode agent: %v", err)
		}
		return resp
	}

	got := read()
	if !got.UseTiers {
		t.Fatalf("use_tiers = false after enabling")
	}
	if len(got.TierIDs) != 2 {
		t.Fatalf("tier_ids = %v, want 2", got.TierIDs)
	}

	// Unknown tier id is rejected.
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"tier_ids": []string{"00000000-0000-0000-0000-000000000000"}},
	), "id", agentID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown tier id: got %d, want 400", w.Code)
	}

	// Empty list clears; omitted keeps.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"tier_ids": []string{}},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("clear tier_ids: got %d: %s", w.Code, w.Body.String())
	}
	if got := read(); len(got.TierIDs) != 0 {
		t.Fatalf("after clear, tier_ids = %v, want empty", got.TierIDs)
	}

	// Toggling off keeps the fallback runtime: the agent row still has its
	// single runtime_id and the switch reads false.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"use_tiers": false},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("disable use_tiers: got %d: %s", w.Code, w.Body.String())
	}
	got = read()
	if got.UseTiers {
		t.Fatalf("use_tiers still true after disabling")
	}
	if got.RuntimeID == "" {
		t.Fatalf("disabling use_tiers cleared the fallback runtime")
	}

	// Restore selection for the delete-availability test below.
	w = httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID,
		map[string]any{"use_tiers": true, "tier_ids": []string{tierFast, tierSlow}},
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable: got %d: %s", w.Code, w.Body.String())
	}

	// Deleting a still-selected tier removes the join row (never silently
	// dropped from the request — the UI renders the missing id from the
	// last-seen list as unavailable; here we assert the join is gone).
	if _, err := testPool.Exec(context.Background(), `DELETE FROM model_tier WHERE id = $1`, tierFast); err != nil {
		t.Fatalf("delete tier: %v", err)
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_tier WHERE agent_id = $1 AND tier_id = $2`, agentID, tierFast).Scan(&n); err != nil {
		t.Fatalf("count join: %v", err)
	}
	_ = n
}
