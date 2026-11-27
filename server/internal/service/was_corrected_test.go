package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// TestWasCorrectedTaskDecodesContextFlag pins the claim-time read: the flag
// lives in the task context JSONB the enqueue stamped, nothing else.
func TestWasCorrectedTaskDecodesContextFlag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  []byte
		want bool
	}{
		{"flag set", []byte(`{"was_corrected":true}`), true},
		{"flag with siblings", []byte(`{"head_sha":"abc","was_corrected":true}`), true},
		{"empty context", []byte(`{}`), false},
		{"fresh rerun only", []byte(`{"fresh_rerun":true}`), false},
		{"null context", nil, false},
		{"garbage context", []byte(`not json`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := WasCorrectedTask(db.AgentTaskQueue{Context: tc.raw}); got != tc.want {
				t.Errorf("WasCorrectedTask(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestIsBackStepCorrectionReviewShape pins the Architect-approved predicate
// half that needs no database: out of a review step into a non-terminal step
// is a correction; everything else here is not. A bare service is enough —
// the review shape returns before any query.
func TestIsBackStepCorrectionReviewShape(t *testing.T) {
	t.Parallel()
	svc := &TaskService{}
	issue := func(status string) db.Issue { return db.Issue{Status: status} }
	for _, tc := range []struct {
		name string
		prev string
		next string
		want bool
	}{
		{"in_review sent back to in_progress", "in_review", "in_progress", true},
		{"agent_review_deploy sent back to todo", "agent_review_deploy", "todo", true},
		{"in_review promoted to done is not a correction", "in_review", "done", false},
		{"in_review cancelled is not a correction", "in_review", "cancelled", false},
		{"same status is not a correction", "in_review", "in_review", false},
		{"empty previous is not a correction", "", "in_progress", false},
		{"forward move is not a correction", "todo", "in_progress", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := svc.isBackStepCorrection(context.Background(), tc.prev, issue(tc.next)); got != tc.want {
				t.Errorf("isBackStepCorrection(%q -> %q) = %v, want %v", tc.prev, tc.next, got, tc.want)
			}
		})
	}
}

// wasCorrectedTestSetup seeds a workspace with an owner member, an online
// agent and one member-created issue assigned to that agent.
func wasCorrectedTestSetup(t *testing.T) (*TaskService, db.Issue, string, string) {
	t.Helper()
	ctx := context.Background()
	pool := newResolveOriginatorPool(t)
	q := db.New(pool)
	suffix := time.Now().UnixNano()
	boot := testutil.New(pool, "", "")
	userID := boot.User(t, "wc-user", fmt.Sprintf("wc-%d@multica.test", suffix))
	workspaceID := boot.Workspace(t, "wc ws", fmt.Sprintf("wc-%d", suffix))
	f := testutil.New(pool, workspaceID, userID)
	f.Member(t, workspaceID, userID, "owner")
	if err := q.SeedIssueStatusEntries(ctx, util.MustParseUUID(workspaceID)); err != nil {
		t.Fatalf("seed statuses: %v", err)
	}
	runtimeID := f.Runtime(t, "wc runtime")
	agentID := f.Agent(t, "wc agent", runtimeID)
	issueID := f.Issue(t, "wc issue", testutil.Cols{
		"assignee_type": "agent", "assignee_id": agentID, "status": "todo",
	})
	issue, err := q.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	svc := NewTaskService(q, pool, nil, events.New())
	return svc, issue, agentID, issueID
}

// TestRerunStampsWasCorrected pins M-604 trigger 1: a manual rerun that keeps
// continuity is a correction; an explicit fresh rerun is not.
func TestRerunStampsWasCorrected(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	svc, issue, _, _ := wasCorrectedTestSetup(t)
	actor := issue.CreatorID

	orig, err := svc.EnqueueTaskForIssue(ctx, issue)
	if err != nil {
		t.Fatalf("EnqueueTaskForIssue (original): %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed', completed_at=now() WHERE id=$1`, orig.ID); err != nil {
		t.Fatalf("fail original: %v", err)
	}

	rerun, err := svc.RerunIssue(ctx, issue.ID, orig.ID, pgtype.UUID{}, actor, false, nil)
	if err != nil {
		t.Fatalf("RerunIssue: %v", err)
	}
	if !WasCorrectedTask(*rerun) {
		t.Errorf("non-fresh rerun context = %s, want was_corrected", string(rerun.Context))
	}

	fresh, err := svc.RerunIssue(ctx, issue.ID, orig.ID, pgtype.UUID{}, actor, true, nil)
	if err != nil {
		t.Fatalf("RerunIssue fresh: %v", err)
	}
	if WasCorrectedTask(*fresh) {
		t.Errorf("fresh rerun context = %s, want no was_corrected", string(fresh.Context))
	}
}

// TestCommentOnFailedCardStampsWasCorrected pins M-604 trigger 4: a member
// comment on a card whose latest run failed starts a correction run. A member
// comment on a healthy card is discussion, and an agent's own chatter is
// never a correction.
func TestCommentOnFailedCardStampsWasCorrected(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	svc, issue, agentID, issueID := wasCorrectedTestSetup(t)
	f := testutil.New(pool, util.UUIDToString(issue.WorkspaceID), util.UUIDToString(issue.CreatorID))

	failRun := func() {
		task, err := svc.EnqueueTaskForIssue(ctx, issue)
		if err != nil {
			t.Fatalf("EnqueueTaskForIssue: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed', completed_at=now() WHERE id=$1`, task.ID); err != nil {
			t.Fatalf("fail run: %v", err)
		}
	}
	completeRun := func(id pgtype.UUID) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, id); err != nil {
			t.Fatalf("complete run: %v", err)
		}
	}

	memberComment := f.Comment(t, issueID, "this is wrong, redo it")
	failRun()
	corrected, err := svc.EnqueueTaskForIssue(ctx, issue, util.MustParseUUID(memberComment))
	if err != nil {
		t.Fatalf("EnqueueTaskForIssue on failed card: %v", err)
	}
	if !WasCorrectedTask(corrected) {
		t.Errorf("member comment on failed card: context = %s, want was_corrected", string(corrected.Context))
	}
	completeRun(corrected.ID)

	// Healthy card: the latest terminal run completed, so a member comment
	// is discussion, not correction.
	healthyComment := f.Comment(t, issueID, "looks good, one more thing")
	plain, err := svc.EnqueueTaskForIssue(ctx, issue, util.MustParseUUID(healthyComment))
	if err != nil {
		t.Fatalf("EnqueueTaskForIssue on healthy card: %v", err)
	}
	if WasCorrectedTask(plain) {
		t.Errorf("member comment on healthy card: context = %s, want no was_corrected", string(plain.Context))
	}
	completeRun(plain.ID)

	// Agent-authored chatter on a failed card is never a correction.
	agentCommentID := f.Comment(t, issueID, "noting progress", testutil.Cols{"author_type": "agent", "author_id": agentID})
	failRun()
	agentTask, err := svc.EnqueueTaskForIssue(ctx, issue, util.MustParseUUID(agentCommentID))
	if err != nil {
		t.Fatalf("EnqueueTaskForIssue agent comment: %v", err)
	}
	if WasCorrectedTask(agentTask) {
		t.Errorf("agent comment on failed card: context = %s, want no was_corrected", string(agentTask.Context))
	}
}

// TestAskPlanResumeStampsWasCorrected pins M-604 trigger 2 at the enqueue
// seam: wasCorrected=true (changes_requested verdict) stamps the flag,
// false (approve/cancel/answer resumes) does not. The verdict→bool mapping
// itself lives at the ReviewPlan call site, pinned end-to-end by the handler
// verdict test.
func TestAskPlanResumeStampsWasCorrected(t *testing.T) {
	ctx := context.Background()
	pool := newResolveOriginatorPool(t)
	svc, issue, agentID, issueID := wasCorrectedTestSetup(t)
	f := testutil.New(pool, util.UUIDToString(issue.WorkspaceID), util.UUIDToString(issue.CreatorID))
	agent, err := svc.Queries.GetAgent(ctx, util.MustParseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	parkedID := f.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": util.UUIDToString(agent.RuntimeID), "status": "blocked",
	})
	parked, err := svc.Queries.GetAgentTask(ctx, util.MustParseUUID(parkedID))
	if err != nil {
		t.Fatalf("load parked task: %v", err)
	}
	refID := dbid.NewV7()
	for _, tc := range []struct {
		name string
		flag bool
		want bool
	}{
		{"changes_requested resume", true, true},
		{"approve/cancel/answer resume", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := svc.enqueueAskPlanResumeTask(ctx, issue, parked,
				"plan_verdict", refID, "handoff", issue.CreatorID, tc.flag)
			if err != nil {
				t.Fatalf("enqueueAskPlanResumeTask: %v", err)
			}
			if got := WasCorrectedTask(task); got != tc.want {
				t.Errorf("wasCorrected=%v: context = %s, want flag %v", tc.flag, string(task.Context), tc.want)
			}
			if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE id=$1`, task.ID); err != nil {
				t.Fatalf("complete resume run: %v", err)
			}
		})
	}
}

// seedBackStepProject creates a project whose workflow sends in_review back
// to in_progress (Needs changes) and in_progress back to todo.
func seedBackStepProject(t *testing.T, f *testutil.Fixture, workspaceID string) string {
	t.Helper()
	ctx := context.Background()
	steps := `[{"status_key":"todo"},{"status_key":"in_progress","back_status_key":"todo"},{"status_key":"in_review","back_status_key":"in_progress"}]`
	var wfID string
	if err := f.Pool.QueryRow(ctx,
		`INSERT INTO issue_workflow (workspace_id, name, initial_status_key, steps) VALUES ($1, 'wc flow', 'todo', $2::jsonb) RETURNING id`,
		workspaceID, steps).Scan(&wfID); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	projectID := f.Project(t, "wc project")
	if _, err := f.Pool.Exec(ctx, `UPDATE project SET workflow_id=$2 WHERE id=$1`, projectID, wfID); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	return projectID
}

// TestIsBackStepCorrectionBackKeyShape pins the Needs-changes half of M-604
// trigger 3 against a real workflow: the exact back-step counts, anything
// else does not.
func TestIsBackStepCorrectionBackKeyShape(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	svc, _, _, _ := wasCorrectedTestSetup(t)
	boot := testutil.New(pool, "", "")
	wsID := boot.Workspace(t, "wc ws2", "wc-backstep")
	f := testutil.New(pool, wsID, "")
	projectID := seedBackStepProject(t, f, wsID)
	issue := db.Issue{
		WorkspaceID: util.MustParseUUID(wsID),
		ProjectID:   util.MustParseUUID(projectID),
	}
	for _, tc := range []struct {
		name string
		prev string
		next string
		want bool
	}{
		{"needs-changes back-step", "in_progress", "todo", true},
		{"forward move is not a correction", "in_progress", "in_review", false},
		{"unrelated status is not a correction", "todo", "in_review", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue.Status = tc.next
			if got := svc.isBackStepCorrection(ctx, tc.prev, issue); got != tc.want {
				t.Errorf("isBackStepCorrection(%q -> %q) = %v, want %v", tc.prev, tc.next, got, tc.want)
			}
		})
	}
}

// TestWillEnqueueRunMarksBackStepCorrection pins M-604 trigger 3 on the
// write path: a handoff-style write that moves the card backwards enqueues a
// run flagged as a correction; the same shape moving forward does not.
func TestWillEnqueueRunMarksBackStepCorrection(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	svc, setupIssue, agentID, _ := wasCorrectedTestSetup(t)
	trigSvc := &IssueService{Queries: q, TaskService: svc}
	wsID := util.UUIDToString(setupIssue.WorkspaceID)
	f := testutil.New(pool, wsID, util.UUIDToString(setupIssue.CreatorID))
	projectID := seedBackStepProject(t, f, wsID)

	// The card sits in_review under the agent; the write moves it back to
	// in_progress and hands it off (assignee change), which is what a
	// Needs-changes move does.
	issueID := f.Issue(t, "sent-back card", testutil.Cols{
		"assignee_type": "agent", "assignee_id": agentID,
		"status": "in_review", "project_id": projectID,
	})
	newAssignee := f.Agent(t, "wc agent 2", f.Runtime(t, "wc runtime 2"))
	issue, err := q.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	move := IssueTriggerInput{
		Issue:              issue,
		PrevStatus:         "in_review",
		StatusChanged:      true,
		AssigneeChanged:    true,
		EnteredHandoffStep: true,
	}
	move.Issue.AssigneeID = util.MustParseUUID(newAssignee)
	move.Issue.Status = "in_progress"
	back, willRun := trigSvc.WillEnqueueRun(ctx, move, IssueTriggerProbe{})
	if !willRun {
		t.Fatalf("back-step handoff write enqueued no run")
	}
	if !back.WasCorrection {
		t.Errorf("back-step write: WasCorrection = false, want true")
	}

	// Same shape moving forward (todo -> in_progress with a handoff reassign)
	// enqueues an ordinary run.
	issue.Status = "in_progress"
	fwd, willRun := trigSvc.WillEnqueueRun(ctx, IssueTriggerInput{
		Issue:              issue,
		PrevStatus:         "todo",
		StatusChanged:      true,
		AssigneeChanged:    true,
		EnteredHandoffStep: true,
	}, IssueTriggerProbe{})
	if !willRun {
		t.Fatalf("forward handoff write enqueued no run")
	}
	if fwd.WasCorrection {
		t.Errorf("forward write: WasCorrection = true, want false")
	}
}
