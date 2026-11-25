package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-570: the pending sweeper announces a pending record once to workspace
// managers, with the Memory tab in details, and never twice.
func TestMoctPendingSweeperNotifiesManagersOnce(t *testing.T) {
	if testPool == nil {
		t.Skip("no test database")
	}
	ctx := context.Background()
	queries := db.New(testPool)
	wsUUID := util.MustParseUUID(testWorkspaceID)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/memory" || r.URL.Query().Get("status") != "pending" {
			t.Errorf("sweeper read = %s %s", r.Method, r.URL)
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("sweeper read drops the tag filter: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"mem_pending_sweep","workspaceId":"multica:ws:` + testWorkspaceID + `","type":"rule","title":"Sweep probe","body":"waiting","attrs":{"level":"must"},"home":{"kind":"team","ref":"multica:ws:` + testWorkspaceID + `"},"tags":[],"priority":100,"pinned":false,"status":"pending","flag":null,"supersedes":null,"supersededBy":null,"conflictsWith":[],"taskRef":null,"authorKind":"member","authorId":"multica:agent:x","createdAt":"2026-11-25T00:00:00Z","updatedAt":"2026-11-25T00:00:00Z","lastUsedAt":null,"useCount":0,"version":1,"tokens":10}],"total":1,"nextCursor":null}`))
	}))
	defer backend.Close()
	client, err := moct.NewClient(moct.Config{BaseURL: backend.URL})
	if err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	var announced int
	bus.Subscribe("inbox:new", func(e events.Event) { announced++ })

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE workspace_id = $1 AND type = 'memory_pending'`, wsUUID)
		testPool.Exec(ctx, `DELETE FROM moct_pending_notified WHERE workspace_id = $1`, wsUUID)
	})

	if n, err := sweepMoctPendingWorkspace(ctx, queries, bus, client, wsUUID); err != nil || n != 1 {
		t.Fatalf("first round notified = %d, err = %v", n, err)
	}

	var row struct {
		Type     string
		Severity string
		Title    string
		Details  []byte
	}
	if err := testPool.QueryRow(ctx, `SELECT type, severity, title, details FROM inbox_item WHERE workspace_id = $1 AND type = 'memory_pending' AND archived = false`, wsUUID).Scan(&row.Type, &row.Severity, &row.Title, &row.Details); err != nil {
		t.Fatalf("inbox row: %v", err)
	}
	if row.Severity != "action_required" {
		t.Fatalf("severity = %q, want action_required", row.Severity)
	}
	var details map[string]string
	if json.Unmarshal(row.Details, &details) != nil || details["memory_id"] != "mem_pending_sweep" || details["tab"] != "moct-memory" {
		t.Fatalf("details = %s", row.Details)
	}
	if announced != 1 {
		t.Fatalf("inbox:new events = %d, want 1", announced)
	}

	// A second round finds the record already announced: silence, not a dup.
	announced = 0
	if n, err := sweepMoctPendingWorkspace(ctx, queries, bus, client, wsUUID); err != nil || n != 0 {
		t.Fatalf("second round notified = %d, err = %v", n, err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE workspace_id = $1 AND type = 'memory_pending' AND archived = false`, wsUUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("inbox rows = %d, err = %v, want exactly 1", count, err)
	}
}
