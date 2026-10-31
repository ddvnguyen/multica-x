package main

import (
	"context"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TestFailStaleTasksDetachedTimeoutIsBounded is the S3-C (M-139) backstop
// regression: a detached run's row must not stay 'running' forever once its
// runtime stops heartbeating, but a runtime that is still heartbeating must
// never have a healthy long run failed by the wall-clock bound. RunningTimeoutSecs
// is set far out so only the detached branch can fire.
func TestFailStaleTasksDetachedTimeoutIsBounded(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	cases := []struct {
		name         string
		detachedCap  bool
		heartbeatAge string // SQL interval for runtime last_seen_at
		timeoutSecs  float64
		wantOldFail  bool
	}{
		{"fresh heartbeat never fails a live run", true, "0 seconds", 3 * 3600, false},
		{"stale heartbeat fails the task past the bound", true, "1 hour", 3 * 3600, true},
		{"stale heartbeat with the bound disabled", true, "1 hour", 0, false},
		{"stale heartbeat without the detached capability", false, "1 hour", 3 * 3600, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The helper creates the issue and first task; the second task is
			// inserted directly. Cleanup also drops the issue — the raw fixture
			// insert leaves number = 0, so leaving it behind would collide with
			// the next test's issue.
			issueID, agentID, oldTaskID := setupSweeperTestFixture(t, "running")
			defer cleanupSweeperFixture(t, issueID, agentID)

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

			// Age one task past the detached bound and keep the other inside it.
			if _, err := testPool.Exec(ctx, `
				UPDATE agent_task_queue SET started_at = now() - interval '4 hours' WHERE id = $1
			`, oldTaskID); err != nil {
				t.Fatalf("age old task: %v", err)
			}

			var previousMetadata *string
			var previousLastSeen *time.Time
			if err := testPool.QueryRow(ctx, `
				SELECT metadata::text, last_seen_at FROM agent_runtime WHERE id = $1
			`, runtimeID).Scan(&previousMetadata, &previousLastSeen); err != nil {
				t.Fatalf("load runtime state: %v", err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `
					UPDATE agent_runtime SET metadata = $2::jsonb, last_seen_at = $3 WHERE id = $1
				`, runtimeID, previousMetadata, previousLastSeen)
			})

			capabilities := `[]`
			if tc.detachedCap {
				capabilities = `["` + protocol.DaemonCapabilityDetachedRuntimeV1 + `"]`
			}
			if _, err := testPool.Exec(ctx, `
				UPDATE agent_runtime
				SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{capabilities}', $2::jsonb),
				    last_seen_at = now() - $3::interval, status = 'online'
				WHERE id = $1
			`, runtimeID, capabilities, tc.heartbeatAge); err != nil {
				t.Fatalf("configure runtime: %v", err)
			}

			failed, err := db.New(testPool).FailStaleTasks(ctx, db.FailStaleTasksParams{
				DispatchTimeoutSecs: 300,
				// Out of reach, so the legacy running branch cannot fire.
				RunningTimeoutSecs:        24 * 3600,
				RuntimeStaleSecs:          staleThresholdSeconds,
				RuntimeReconnectGraceSecs: 600,
				DetachedTimeoutSecs:       tc.timeoutSecs,
			})
			if err != nil {
				t.Fatalf("FailStaleTasks: %v", err)
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
			if oldFailed != tc.wantOldFail {
				t.Errorf("4h-old task failed = %v, want %v", oldFailed, tc.wantOldFail)
			}
			if youngFailed {
				t.Error("a task inside the detached bound must survive the sweep")
			}
		})
	}
}

func TestDetachedTaskTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultDetachedTaskTimeout},
		{"  ", defaultDetachedTaskTimeout},
		{"0", 0},
		{"0s", 0},
		{"-5m", defaultDetachedTaskTimeout},
		{"abc", defaultDetachedTaskTimeout},
		{"1m", minimumRuntimeReconnectGrace},
		{"2h", 2 * time.Hour},
	}
	for _, tc := range cases {
		if got := detachedTaskTimeoutFromEnv(tc.raw); got != tc.want {
			t.Errorf("detachedTaskTimeoutFromEnv(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
