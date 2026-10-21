package handler

import (
	"context"
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestSetAgentRuntimeFreebuffMetadata_MergesWithoutErasing pins the merge
// semantics of the Freebuff block in the runtime's metadata.
//
// Two actors write into that block: the daemon (the account snapshot) and
// the server (the browser-login state). A write that carries only one of
// them must not erase the other — the account snapshot arrives every minute
// and the login state only when a login is running, so an overwrite would
// make the login state flicker away.
func TestSetAgentRuntimeFreebuffMetadata_MergesWithoutErasing(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'freebuff-test-machine', 'Freebuff test runtime', 'local', 'codebuddy', 'online', 'x', '{}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	queries := testHandler.Queries

	// The server records the click.
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"connect":{"state":"requested"}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write connect state: %v", err)
	}

	// The daemon reports the account snapshot. The connect state must survive.
	if err := queries.SetAgentRuntimeFreebuffMetadata(ctx, db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: []byte(`{"status":{"status":"active","remainingMs":120000}}`),
		ID:      parseUUID(runtimeID),
	}); err != nil {
		t.Fatalf("write status: %v", err)
	}

	block := readFreebuffBlock(t, runtimeID)
	if block.Connect == nil || block.Connect.State != "requested" {
		t.Fatalf("connect state was erased by the status report: %#v", block.Connect)
	}
	if len(block.Status) == 0 {
		t.Fatalf("account snapshot missing: %#v", block)
	}
	var status map[string]any
	if err := json.Unmarshal(block.Status, &status); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if status["remainingMs"] != float64(120000) {
		t.Fatalf("remainingMs = %v, want 120000", status["remainingMs"])
	}
}

// readFreebuffBlock reads the Freebuff block back out of a runtime row.
func readFreebuffBlock(t *testing.T, runtimeID string) *FreebuffBlock {
	t.Helper()
	var metadata []byte
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
		t.Fatalf("read runtime metadata: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &envelope); err != nil {
		t.Fatalf("runtime metadata is not JSON: %v", err)
	}
	raw, ok := envelope["freebuff"]
	if !ok {
		t.Fatalf("runtime metadata has no freebuff block: %s", metadata)
	}
	block := &FreebuffBlock{}
	if err := json.Unmarshal(raw, block); err != nil {
		t.Fatalf("freebuff block is not JSON: %v", err)
	}
	return block
}
