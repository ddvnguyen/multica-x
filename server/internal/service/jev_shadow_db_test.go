package service

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
		workspaceID:   workspace,
		decisionPoint: jevShadowDecisionPointWakeup,
		subjectType:   jevShadowSubjectType,
		subjectID:     wakeup,
		issueID:       issue,
		questionKey:   jevShadowQuestionKey,
		answer:        `{"type":"noul","noul":0.81}`,
		threshold:     new(jevShadowThreshold),
		verdict:       "act",
		model:         jev.DefaultModel,
		usage:         `{"input_tokens":231,"output_tokens":9}`,
		latencyMS:     120,
	}
	if err := shadow.insert(ctx, answered); err != nil {
		t.Fatalf("insert an answered observation: %v", err)
	}

	failed := jevShadowRow{workspaceID: workspace, decisionPoint: jevShadowDecisionPointWakeup, subjectType: jevShadowSubjectType, subjectID: wakeup, issueID: issue, questionKey: jevShadowQuestionKey, threshold: new(jevShadowThreshold), latencyMS: 7, errorKind: "timeout"}
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

// M-592: the agreement report runs over seeded rows. One row per exercised
// path: a reclaim agree (task started within the window), a reclaim disagree
// (verdict act, task never started), a route agree (Engineer-Agent assignee),
// a route unmapped (no such agent), a route error, and a wakeup volume row
// (no agreement label by design).
func TestAgreementReportCountsSeededRows(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()

	var table *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.jev_shadow_decision')::text").Scan(&table); err != nil {
		t.Fatalf("look up the table: %v", err)
	}
	if table == nil {
		t.Skip("jev_shadow_decision is not migrated in this database")
	}

	var userID, workspaceID, runtimeID, engineerID, issueID, agreedTaskID, idleTaskID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('jev-agree', 'jev-agree-' || gen_random_uuid() || '@multica.test') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('jev-agree-ws', 'jev-agree-ws-' || gen_random_uuid()) RETURNING id`).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })
	if err := pool.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id) VALUES ($1, 'r', 'cloud', 'codex', 'online', '', '{}'::jsonb, $2) RETURNING id`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id, instructions, custom_env, custom_args) VALUES ($1, 'Engineer-Agent', 'cloud', '{}'::jsonb, $2, 'workspace', 1, $3, '', '{}'::jsonb, '[]'::jsonb) RETURNING id`, workspaceID, runtimeID, userID).Scan(&engineerID); err != nil {
		t.Fatalf("seed engineer: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id) VALUES ($1, 'agree-issue', 'member', $2, 'agent', $3) RETURNING id`, workspaceID, userID, engineerID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at) VALUES ($1, $2, $3, 'running', 0, now()) RETURNING id`, engineerID, runtimeID, issueID).Scan(&agreedTaskID); err != nil {
		t.Fatalf("seed agreed task: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority) VALUES ($1, $2, $3, 'dispatched', 0) RETURNING id`, engineerID, runtimeID, issueID).Scan(&idleTaskID); err != nil {
		t.Fatalf("seed idle task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM jev_shadow_decision WHERE workspace_id = $1::uuid`, workspaceID)
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1::uuid`, issueID)
		pool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1::uuid`, issueID)
		pool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1::uuid`, engineerID)
	})

	shadow := &JevShadow{db: pool, log: discardedLogger(), now: time.Now}
	seed := func(row jevShadowRow) {
		t.Helper()
		row.workspaceID = workspaceID
		if err := shadow.insert(ctx, row); err != nil {
			t.Fatalf("seed shadow row: %v", err)
		}
	}
	// Reclaim: agreed (act + started task), disagreed (act + never started).
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointReclaim, subjectType: jevShadowSubjectTypeTask, subjectID: agreedTaskID, issueID: issueID, questionKey: jevShadowQuestionKeyResume, answer: `{"type":"noul","noul":0.9}`, threshold: new(jevShadowThreshold), verdict: "act", model: "jev-test", usage: `{"input_tokens":100,"output_tokens":5}`, latencyMS: 50})
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointReclaim, subjectType: jevShadowSubjectTypeTask, subjectID: idleTaskID, issueID: issueID, questionKey: jevShadowQuestionKeyResume, answer: `{"type":"noul","noul":0.8}`, threshold: new(jevShadowThreshold), verdict: "act", model: "jev-test", latencyMS: 60})
	// Route: agreed (engineer pick + engineer assignee), unmapped (no issue),
	// error (timeout).
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointRoute, subjectType: "issue", subjectID: issueID, issueID: issueID, questionKey: jevShadowQuestionKeyRoute, answer: `{"type":"choice","choice":"engineer","probabilities":{"engineer":0.7},"confidence":0.4}`, verdict: "engineer", model: "jev-test", latencyMS: 70})
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointRoute, subjectType: "issue", subjectID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", issueID: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", questionKey: jevShadowQuestionKeyRoute, answer: `{"type":"choice","choice":"reviewer","probabilities":{"reviewer":0.6},"confidence":0.2}`, verdict: "reviewer", model: "jev-test", latencyMS: 80})
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointRoute, subjectType: "issue", subjectID: issueID, issueID: issueID, questionKey: jevShadowQuestionKeyRoute, latencyMS: 5, errorKind: "timeout"})
	// Wakeup: volume only.
	seed(jevShadowRow{decisionPoint: jevShadowDecisionPointWakeup, subjectType: jevShadowSubjectType, subjectID: "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb", issueID: issueID, questionKey: jevShadowQuestionKey, answer: `{"type":"noul","noul":0.3}`, threshold: new(jevShadowThreshold), verdict: "no_action", model: "jev-test", latencyMS: 40})

	report, err := os.ReadFile("../../scripts/jev-shadow-agreement.sql")
	if err != nil {
		t.Fatalf("read the report: %v", err)
	}
	rows, err := pool.Query(ctx, string(report), pgtype.Int4{Int32: 15, Valid: true})
	if err != nil {
		t.Fatalf("run the report: %v", err)
	}
	defer rows.Close()
	got := map[string][5]int64{}
	for rows.Next() {
		var point, key string
		var total, agreed, disagreed, unmapped, errors int64
		var meanConf *float64
		var meanLatency *float64
		var tokens int64
		if err := rows.Scan(&point, &key, &total, &agreed, &disagreed, &unmapped, &errors, &meanConf, &meanLatency, &tokens); err != nil {
			t.Fatalf("scan report row: %v", err)
		}
		got[point+"/"+key] = [5]int64{total, agreed, disagreed, unmapped, errors}
		if point == "triage_route_assign" && meanConf == nil {
			t.Errorf("route rows should carry mean confidence")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate report: %v", err)
	}
	want := map[string][5]int64{
		"stall_resume_reclaim/needs_resume":  {2, 1, 1, 0, 0},
		"triage_route_assign/route_pick":     {3, 1, 0, 1, 1},
		"issue_wakeup_dispatch/needs_action": {1, 0, 0, 0, 0},
	}
	for key, w := range want {
		g, ok := got[key]
		if !ok {
			t.Errorf("report has no row for %s (got %v)", key, got)
			continue
		}
		if g != w {
			t.Errorf("%s = %v (total/agreed/disagreed/unmapped/errors), want %v", key, g, w)
		}
	}
}
