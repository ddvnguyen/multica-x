package main

import (
	"context"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestFailStaleTasksDetachedTimeoutIsBounded is the S3-C (M-139) blocker-1
// regression: a detached-capable runtime's running task must not stay 'running'
// forever just because the restarted daemon keeps heartbeating. The heartbeat
// says nothing about a task executing in its own transient scope, so the wall
// clock (budget + grace) must be the bound.
func TestFailStaleTasksDetachedTimeoutIsBounded(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// One fixture: the helper creates the issue and first task; the second task
	// is inserted directly so the shared issue fixture is only built once.
	issueID, agentID, oldTaskID := setupSweeperTestFixture(t, "running")

	var runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	var youngTaskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at, started_at)
		VALUES ($1, $2, $3, 'running', 0, now() - interval '1 hour', now() - interval '1 hour')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&youngTaskID); err != nil {
		t.Fatalf("create young task: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, taskID := range []string{oldTaskID, youngTaskID} {
			_, _ = testPool.Exec(cleanupCtx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		}
	})

	// Age one task past the detached bound and keep the other inside it.
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET started_at = now() - interval '4 hours' WHERE id = $1
	`, oldTaskID); err != nil {
		t.Fatalf("age old task: %v", err)
	}

	// The runtime advertises the detached capability and is heartbeating now:
	// exactly the state that used to strand the orphan forever.
	var previousMetadata *string
	if err := testPool.QueryRow(ctx, `SELECT metadata::text FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&previousMetadata); err != nil {
		t.Fatalf("load runtime metadata: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			UPDATE agent_runtime SET metadata = $2::jsonb WHERE id = $1
		`, runtimeID, previousMetadata)
	})
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{capabilities}', '["detached-runtime-v1"]'::jsonb),
		    last_seen_at = now(), status = 'online'
		WHERE id = $1
	`, runtimeID); err != nil {
		t.Fatalf("mark runtime detached: %v", err)
	}

	queries := db.New(testPool)

	// Detached branch disabled: the fresh heartbeat wins, as before this change.
	disabled, err := queries.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs:       300,
		RunningTimeoutSecs:        9000,
		RuntimeStaleSecs:          staleThresholdSeconds,
		RuntimeReconnectGraceSecs: defaultRuntimeReconnectGrace.Seconds(),
		DetachedTimeoutSecs:       0,
	})
	if err != nil {
		t.Fatalf("FailStaleTasks (disabled): %v", err)
	}
	for _, task := range disabled {
		if task.ID.Bytes == parseUUIDBytes(oldTaskID) || task.ID.Bytes == parseUUIDBytes(youngTaskID) {
			t.Fatalf("task %s failed with the detached branch disabled", task.ID)
		}
	}

	// Detached branch enabled at 3h: the 4h task fails, the 1h task survives.
	failed, err := queries.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs:       300,
		RunningTimeoutSecs:        9000,
		RuntimeStaleSecs:          staleThresholdSeconds,
		RuntimeReconnectGraceSecs: defaultRuntimeReconnectGrace.Seconds(),
		// The production default is the running budget plus the detached
		// grace; 3h here keeps the fixture cheap to age.
		DetachedTimeoutSecs: 3 * 3600,
	})
	if err != nil {
		t.Fatalf("FailStaleTasks (detached bound): %v", err)
	}
	var oldFailed, youngFailed bool
	for _, task := range failed {
		switch {
		case task.ID.Bytes == parseUUIDBytes(oldTaskID):
			oldFailed = true
			if task.FailureReason.String != "timeout" {
				t.Errorf("failure reason = %q, want timeout", task.FailureReason.String)
			}
		case task.ID.Bytes == parseUUIDBytes(youngTaskID):
			youngFailed = true
		}
	}
	if !oldFailed {
		t.Fatal("a detached task past its wall-clock bound must fail even with a fresh heartbeat")
	}
	if youngFailed {
		t.Fatal("a detached task inside its bound must survive the sweep")
	}
}
