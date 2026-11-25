package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/jev"
)

// The insert is hand-written SQL, so the one thing a fake DBTX cannot prove is
// that Postgres accepts it: the casts, the NULLIFs and the jsonb columns all
// only fail at parse or execution time. This test runs the real statement —
// once with an answer, once with an error kind — and reads the rows back.
//
// It skips when the database is unreachable, like the rest of this package, and
// when the migration has not run there (a lane database behind head), because a
// missing table is not a defect in this change.
func TestShadowInsertWritesRowsAgainstPostgres(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()

	var table *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.jev_shadow_decision')::text").Scan(&table); err != nil {
		t.Fatalf("look up the table: %v", err)
	}
	if table == nil {
		t.Skip("jev_shadow_decision is not migrated in this database")
	}

	workspaceID := jevTestUUID("55555555-5555-4555-8555-555555555555").Bytes
	workspace := uuidText(workspaceID)
	wakeup := "66666666-6666-4666-8666-666666666666"
	issue := "77777777-7777-4777-8777-777777777777"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jev_shadow_decision WHERE workspace_id = $1::uuid", workspace)
	})

	shadow := &JevShadow{db: pool, log: discardedLogger(), now: time.Now}

	answered := jevShadowRow{
		workspaceID: workspace,
		wakeupID:    wakeup,
		issueID:     issue,
		answer:      `{"type":"noul","noul":0.81}`,
		threshold:   jevShadowThreshold,
		verdict:     "act",
		model:       jev.DefaultModel,
		usage:       `{"input_tokens":231,"output_tokens":9}`,
		latencyMS:   120,
	}
	if err := shadow.insert(ctx, answered); err != nil {
		t.Fatalf("insert an answered observation: %v", err)
	}

	failed := jevShadowRow{workspaceID: workspace, wakeupID: wakeup, issueID: issue, threshold: jevShadowThreshold, latencyMS: 7, errorKind: "timeout"}
	if err := shadow.insert(ctx, failed); err != nil {
		t.Fatalf("insert a failed observation: %v", err)
	}

	var answeredCount, failedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE verdict = 'act' AND answer IS NOT NULL AND usage IS NOT NULL AND error_kind IS NULL),
		       count(*) FILTER (WHERE error_kind = 'timeout' AND answer IS NULL AND verdict IS NULL)
		  FROM jev_shadow_decision WHERE workspace_id = $1::uuid`, workspace).Scan(&answeredCount, &failedCount); err != nil {
		t.Fatalf("read the rows back: %v", err)
	}
	if answeredCount != 1 {
		t.Errorf("answered rows = %d, want 1", answeredCount)
	}
	if failedCount != 1 {
		t.Errorf("failed rows = %d, want 1", failedCount)
	}

	var answer, model string
	var threshold float64
	if err := pool.QueryRow(ctx, `
		SELECT answer::text, model, threshold FROM jev_shadow_decision
		 WHERE workspace_id = $1::uuid AND issue_id = $2::uuid AND error_kind IS NULL`, workspace, issue,
	).Scan(&answer, &model, &threshold); err != nil {
		t.Fatalf("read the answered row: %v", err)
	}
	if !strings.Contains(answer, "0.81") {
		t.Errorf("answer = %q, want the vendor's answer", answer)
	}
	if model != jev.DefaultModel {
		t.Errorf("model = %q, want %s", model, jev.DefaultModel)
	}
	if threshold != jevShadowThreshold {
		t.Errorf("threshold = %v, want %v", threshold, jevShadowThreshold)
	}
	if !json.Valid([]byte(answer)) {
		t.Errorf("answer is not json: %q", answer)
	}
}

// uuidText renders the canonical form Postgres expects, from an already-parsed
// UUID's bytes.
func uuidText(raw [16]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range raw {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hex[b>>4], hex[b&0x0f])
	}
	return string(out)
}
