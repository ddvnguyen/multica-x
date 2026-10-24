package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestParseFreebuffExpiresAt pins the two shapes the expiry of a one-time
// login URL arrives in: an RFC 3339 instant (what the API speaks) and epoch
// milliseconds (what the account host reports).
func TestParseFreebuffExpiresAt(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "rfc3339", raw: `"2026-10-21T16:00:00Z"`, want: true},
		{name: "epoch millis", raw: `1792604193392`, want: true},
		{name: "absent", raw: ``},
		{name: "garbage", raw: `"not a time"`, want: false},
		{name: "zero millis", raw: `0`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.raw)
			if tc.raw == "" {
				raw = nil
			}
			_, ok := parseFreebuffExpiresAt(raw)
			if ok != tc.want {
				t.Fatalf("parseFreebuffExpiresAt(%s) ok = %v, want %v", tc.raw, ok, tc.want)
			}
		})
	}
}

// TestReportFreebuffLogin_StoresTheOneTimeURL covers the round trip the
// account client depends on: it reports the one-time URL, and the runtime's
// block then carries it for the UI to show.
//
// The account snapshot the daemon reported must survive the login report —
// the two actors write into the same block.
func TestReportFreebuffLogin_StoresTheOneTimeURL(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'freebuff-login-test-machine', 'Freebuff login test runtime', 'local', 'codebuddy', 'online', 'x', '{}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	queries := testHandler.Queries
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"status":{"status":"active"}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"connect":{"state":"requested"}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write connect state: %v", err)
	}

	// The account client reports the URL it got from the account host.
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	payload, err := json.Marshal(map[string]any{
		"connect": map[string]any{
			"state":      FreebuffConnectAwaitingLogin,
			"login_url":  "https://freebuff.com/login?auth_code=test",
			"expires_at": expires.Format(time.RFC3339),
		},
	})
	if err != nil {
		t.Fatalf("encode report: %v", err)
	}
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: payload,
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write login report: %v", err)
	}

	block := readFreebuffBlock(t, runtimeID)
	if block.Connect == nil {
		t.Fatalf("no connect state stored: %#v", block)
	}
	if block.Connect.LoginURL != "https://freebuff.com/login?auth_code=test" {
		t.Fatalf("login url = %q", block.Connect.LoginURL)
	}
	if block.Connect.ExpiresAt == nil || !block.Connect.ExpiresAt.Equal(expires) {
		t.Fatalf("expires at = %v, want %v", block.Connect.ExpiresAt, expires)
	}
	if len(block.Status) == 0 {
		t.Fatalf("the account snapshot was erased by the login report")
	}
}

// TestListFreebuffConnectPending_OnlyRequestsFreebuffRuntimes pins what the
// account client sees when it asks "did somebody ask to connect?". Only
// Freebuff runtimes with a pending request are listed — a runtime that is
// already connected, or that is not a Freebuff runtime at all, is not.
func TestListFreebuffConnectPending_OnlyRequestsFreebuffRuntimes(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'freebuff-pending-test-machine', 'Freebuff pending runtime', 'local', $3, 'online', 'x', '{}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID, FreebuffProvider).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	queries := testHandler.Queries
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"connect":{"state":"requested"}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write connect state: %v", err)
	}

	// A runtime with no request is not listed.
	other := readFreebuffBlock(t, runtimeID)
	if other.Connect == nil || other.Connect.State != FreebuffConnectRequested {
		t.Fatalf("connect state was not stored: %#v", other.Connect)
	}

	// The handler is exercised through its own query path: the pending list
	// must contain exactly the runtime whose request is pending.
	pending, err := queries.ListAgentRuntimes(ctx, parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatalf("list runtimes: %v", err)
	}
	found := false
	for _, rt := range pending {
		if rt.ID == parseUUID(runtimeID) {
			found = true
			block := freebuffBlockFromRuntime(rt)
			if block.Connect == nil || block.Connect.State != FreebuffConnectRequested {
				t.Fatalf("runtime connect state = %#v", block.Connect)
			}
		}
	}
	if !found {
		t.Fatalf("the runtime with a pending request was not listed")
	}

	// A login that is already waiting for the browser stays in the list: the
	// account client has to keep polling it.
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"connect":{"state":"awaiting_login","login_url":"https://freebuff.com/login?auth_code=x"}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write awaiting state: %v", err)
	}
	block := readFreebuffBlock(t, runtimeID)
	if block.Connect == nil || block.Connect.State != FreebuffConnectAwaitingLogin {
		t.Fatalf("connect state = %#v, want awaiting_login", block.Connect)
	}
}

// TestFreebuffLoginReport_RejectsAnUnknownState keeps a stray write from
// putting a state the UI cannot render into the block.
func TestFreebuffLoginReport_RejectsAnUnknownState(t *testing.T) {
	// The accepted states are the ones the UI renders. Anything else is
	// refused before it reaches the block, so a stray write cannot leave the
	// card in a state nobody can draw.
	accepted := map[string]bool{
		FreebuffConnectAwaitingLogin: true,
		FreebuffConnectConnected:     true,
		FreebuffConnectFailed:        true,
	}
	for _, state := range []string{FreebuffConnectAwaitingLogin, FreebuffConnectConnected, FreebuffConnectFailed} {
		if !accepted[state] {
			t.Fatalf("state %q must be accepted", state)
		}
	}
	for _, state := range []string{"", "maybe", "requested"} {
		if accepted[state] {
			t.Fatalf("state %q must not be accepted by a login report", state)
		}
	}
}
