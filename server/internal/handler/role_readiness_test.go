package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// roleForReadinessTest is a model-free Role that is invocable by the test
// member, so the @mention/chat invocation gate cannot be the reason a verdict
// changes — only AgentReadiness can.
func roleForReadinessTest(t *testing.T) string {
	t.Helper()
	roleID := dbfx.Agent(t, roleTestName("m510-readiness-role"), "", testutil.Cols{
		"subtype":         "role",
		"tier_role":       "engineer",
		"permission_mode": "public_to",
	})
	dbfx.Exec(t, `INSERT INTO agent_invocation_target (agent_id, target_type, target_id, created_by)
		VALUES ($1, 'workspace', $2, $3)`, roleID, testWorkspaceID, testUserID)
	return roleID
}

// TestRoleMention_ReadinessFlagGate is the real @mention admission path: a
// flag-on Role passes readiness (and reaches the tier resolver); flag-off
// stays blocked with agent_runtime_required, exactly like a runtime-less Agent.
func TestRoleMention_ReadinessFlagGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	roleID := roleForReadinessTest(t)
	issueID := createMentionFixtureIssue(t, context.Background(), "M-510 readiness mention "+roleTestName(""))

	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	if code := mentionAgentReasonCode(t, issueID, roleID); code != string(ReasonAgentRuntimeRequired) {
		t.Fatalf("flag-off Role @mention reason = %q, want %q", code, ReasonAgentRuntimeRequired)
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	if code := mentionAgentReasonCode(t, issueID, roleID); code != "" {
		t.Fatalf("flag-on Role @mention was blocked with %q", code)
	}
}

// TestRoleChatSend_ReadinessFlagGate is the chat-send admission path; the
// assertion is the same gate from the other handler that calls AgentReadiness.
func TestRoleChatSend_ReadinessFlagGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	roleID := roleForReadinessTest(t)

	send := func(sessionID string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/messages",
			map[string]any{"content": "hello from the readiness test"})
		req = withURLParam(req, "sessionId", sessionID)
		req = withChatTestWorkspaceCtx(t, req)
		testHandler.SendChatMessage(w, req)
		return w
	}

	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	blocked := send(createHandlerTestChatSession(t, roleID))
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), string(ReasonAgentRuntimeRequired)) {
		t.Fatalf("flag-off Role chat send = %d: %s, want 409 agent_runtime_required", blocked.Code, blocked.Body.String())
	}

	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	allowed := send(createHandlerTestChatSession(t, roleID))
	if allowed.Code == http.StatusConflict && strings.Contains(allowed.Body.String(), string(ReasonAgentRuntimeRequired)) {
		t.Fatalf("flag-on Role chat send was blocked with agent_runtime_required: %d: %s", allowed.Code, allowed.Body.String())
	}
}
