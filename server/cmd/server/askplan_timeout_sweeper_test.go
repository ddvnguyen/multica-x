package main

// M-131/M-332 — ask/plan timeout sweeper tests.
//
// Each fixture parks one task with live evidence (an open card or an in_review
// plan) aged past (or inside) its effective timeout, then drives
// sweepExpiredAskPlanParks directly: the round function is the sweeper's whole
// behavior — the 30s ticker around it is already covered by
// TestPeriodicSweepStopsWithItsContext. The assertions name the contracts the
// M-332 ladder pins:
//
//  1. the deadline ESCALATES, it does not cancel: the parked task stays
//     'blocked', the card stays open / the plan stays in review, and nothing is
//     resumed by the sweep itself;
//  2. escalation happens once — a second round posts no second comment and
//     triggers no second lead run;
//  3. escalation is one durable system comment that states the lead's rule, plus
//     exactly one trigger: a mention task for an agent lead, an action-required
//     inbox row for a member lead, nothing invented when no lead is set — and no
//     Triage bypass;
//  4. first answer wins, whoever answers: the Owner's answer after an escalation
//     still resumes the original parked run, a lead's answer does the same and is
//     recorded as the lead's (answered_by_type='agent'), and whichever lands
//     second loses with ErrQuestionCardClosed.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type askPlanSweepOpts struct {
	// plan parks on an in_review plan instead of an open question card.
	plan bool
	// answered leaves the card already answered — a park whose human replied
	// before any sweep saw it. Never a candidate, never escalated.
	answered bool
	parkAge  time.Duration
	// workspaceSettings / projectSettings are raw JSONB carrying (or omitting)
	// ask_plan_timeout_seconds; empty means leave the row untouched.
	workspaceSettings string
	projectSettings   string
	// leadType: "" (none), "member" (the fixture user), "agent" (the
	// integration test agent).
	leadType string
	triage   bool
}

type askPlanSweepFixture struct {
	issueID       string
	projectID     string
	taskID        string
	cardID        string
	planID        string
	parkedAgentID string
	leadAgentID   string
}

// setupAskPlanSweepFixture builds a park with live evidence. Every row it
// creates is removed on cleanup and the workspace settings row is restored, so
// the candidate set each test sweeps is exactly its own fixture.
func setupAskPlanSweepFixture(t *testing.T, opts askPlanSweepOpts) askPlanSweepFixture {
	t.Helper()
	ctx := context.Background()

	// Deterministic lead: the agent TestMain seeds, never an arbitrary
	// workspace agent (other suites insert their own).
	var leadAgentID, runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id FROM agent a
		WHERE a.workspace_id = $1 AND a.name = 'Integration Test Agent'
		LIMIT 1
	`, testWorkspaceID).Scan(&leadAgentID, &runtimeID); err != nil {
		t.Fatalf("find integration test agent: %v", err)
	}
	fx := askPlanSweepFixture{leadAgentID: leadAgentID}

	// The parked run's own agent — distinct from the lead, so "the lead got a
	// task" and "the parked run got no resume" are separately assertable.
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'M-131 parked agent', '', 'cloud', '{}'::jsonb, $2, 'workspace', 1, $3)
		RETURNING id
	`, testWorkspaceID, runtimeID, testUserID).Scan(&fx.parkedAgentID); err != nil {
		t.Fatalf("create parked agent: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, fx.parkedAgentID)
	})

	if opts.workspaceSettings != "" {
		var original string
		if err := testPool.QueryRow(ctx, `SELECT settings::text FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&original); err != nil {
			t.Fatalf("read workspace settings: %v", err)
		}
		if _, err := testPool.Exec(ctx, `UPDATE workspace SET settings = $2::jsonb WHERE id = $1`, testWorkspaceID, opts.workspaceSettings); err != nil {
			t.Fatalf("set workspace settings: %v", err)
		}
		t.Cleanup(func() {
			testPool.Exec(context.Background(), `UPDATE workspace SET settings = $2::jsonb WHERE id = $1`, testWorkspaceID, original)
		})
	}

	var leadType, leadID any
	switch opts.leadType {
	case "member":
		leadType, leadID = "member", testUserID
	case "agent":
		leadType, leadID = "agent", fx.leadAgentID
	}
	if opts.projectSettings == "" {
		opts.projectSettings = "{}"
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, settings, lead_type, lead_id)
		VALUES ($1, 'M-131 sweep project', $2::jsonb, $3, $4)
		RETURNING id
	`, testWorkspaceID, opts.projectSettings, leadType, leadID).Scan(&fx.projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, fx.projectID)
	})

	var triage any
	if opts.triage {
		triage = "pending"
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id,
			project_id, position, number, triage_state)
		VALUES ($1, 'M-131 sweep issue', 'todo', 'none', 'member', $2, $3, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
		        $4)
		RETURNING id
	`, testWorkspaceID, testUserID, fx.projectID, triage).Scan(&fx.issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		// Cards and plans carry no FK to the issue (538 house style) — delete
		// them by issue, then the inbox rows, then the issue itself.
		testPool.Exec(context.Background(), `DELETE FROM agent_question_card WHERE issue_id = $1`, fx.issueID)
		testPool.Exec(context.Background(), `DELETE FROM agent_plan WHERE issue_id = $1`, fx.issueID)
		cleanupInboxForIssue(t, fx.issueID)
		cleanupTestIssue(t, fx.issueID)
	})

	// Task id and evidence id are both pre-generated: the task names the
	// evidence via trigger_evidence_ref_id and the evidence names the task via
	// task_id, and neither row has a foreign key on the other (538 house
	// style), so there is no ordering to satisfy — only both ids to know.
	taskID := dbid.NewV7()
	evidenceID := dbid.NewV7()
	age := opts.parkAge.Seconds()

	kind := "question_answer"
	if opts.plan {
		kind = "plan_verdict"
		if opts.answered {
			t.Fatalf("answered only applies to question cards")
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_plan (id, workspace_id, issue_id, task_id, agent_id, title,
				status, revision, submitted_revision, sections, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'M-131 sweep plan', 'in_review', 1, 1, '[]'::jsonb,
			        now() - make_interval(secs => $6))
			RETURNING id
		`, evidenceID, testWorkspaceID, fx.issueID, taskID, fx.parkedAgentID, age).Scan(&fx.planID); err != nil {
			t.Fatalf("create plan: %v", err)
		}
	} else {
		cardStatus := "open"
		if opts.answered {
			cardStatus = "answered"
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_question_card (id, workspace_id, issue_id, task_id, agent_id,
				status, questions, answers, answered_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6,
			        '[{"prompt":"Pick one?","options":[{"label":"Thursday"},{"label":"Friday"}]}]'::jsonb,
			        CASE WHEN $6 = 'answered' THEN '[{"selected":[1]}]'::jsonb ELSE NULL END,
			        CASE WHEN $6 = 'answered' THEN $7::uuid ELSE NULL END,
			        now() - make_interval(secs => $8),
			        now() - make_interval(secs => $8))
			RETURNING id
		`, evidenceID, testWorkspaceID, fx.issueID, taskID, fx.parkedAgentID, cardStatus, testUserID, age).Scan(&fx.cardID); err != nil {
			t.Fatalf("create question card: %v", err)
		}
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, priority,
			started_at, wait_reason, trigger_evidence_kind, trigger_evidence_ref_id)
		VALUES ($1, $2, $3, $4, 'blocked', 0, now() - make_interval(secs => $5),
		        'awaiting human answer', $6, $7)
		RETURNING id
	`, taskID, fx.parkedAgentID, runtimeID, fx.issueID, age, kind, evidenceID).Scan(&fx.taskID); err != nil {
		t.Fatalf("create parked task: %v", err)
	}
	return fx
}

// sweepAskPlanRound runs exactly one sweeper round against the shared DB.
func sweepAskPlanRound(t *testing.T) int {
	t.Helper()
	queries := db.New(testPool)
	taskSvc := service.NewTaskService(queries, testPool, nil, events.New())
	escalated, err := sweepExpiredAskPlanParks(context.Background(), queries, taskSvc)
	if err != nil {
		t.Fatalf("sweepExpiredAskPlanParks: %v", err)
	}
	return escalated
}

func askPlanTaskService() *service.TaskService {
	return service.NewTaskService(db.New(testPool), testPool, nil, events.New())
}

func askPlanTaskStatus(t *testing.T, taskID string) (status, cancelledBy string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(), `
		SELECT status, COALESCE(cancelled_by_type, '') FROM agent_task_queue WHERE id = $1
	`, taskID).Scan(&status, &cancelledBy); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status, cancelledBy
}

// askPlanSystemComment is the newest system comment on the issue ("" when the
// sweep posted none).
func askPlanSystemComment(t *testing.T, issueID string) string {
	t.Helper()
	var content string
	err := testPool.QueryRow(context.Background(), `
		SELECT content FROM comment
		WHERE issue_id = $1 AND author_type = 'system' AND type = 'system'
		ORDER BY created_at DESC LIMIT 1
	`, issueID).Scan(&content)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read system comment: %v", err)
	}
	return content
}

func askPlanSystemCommentCount(t *testing.T, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND author_type = 'system' AND type = 'system'
	`, issueID).Scan(&n); err != nil {
		t.Fatalf("count system comments: %v", err)
	}
	return n
}

// askPlanQueuedFor returns (task_id, trigger_comment_id) for every queued task
// on the issue — the lead's mention run, or nothing.
func askPlanQueuedFor(t *testing.T, issueID string) [][2]string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT id::text, COALESCE(trigger_comment_id::text, '') FROM agent_task_queue
		WHERE issue_id = $1 AND status = 'queued'
	`, issueID)
	if err != nil {
		t.Fatalf("list queued tasks: %v", err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var id, trigger string
		if err := rows.Scan(&id, &trigger); err != nil {
			t.Fatalf("scan queued task: %v", err)
		}
		out = append(out, [2]string{id, trigger})
	}
	return out
}

func askPlanInboxCount(t *testing.T, issueID, recipientID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND recipient_id = $2 AND recipient_type = 'member' AND type = 'agent_question'
	`, issueID, recipientID).Scan(&n); err != nil {
		t.Fatalf("count inbox items: %v", err)
	}
	return n
}

// askPlanResumeTask is the resume run enqueued for the parked agent ("" when
// none exists) plus its handoff note.
func askPlanResumeTask(t *testing.T, issueID, parkedTaskID string) (resumeID, note string) {
	t.Helper()
	var id string
	var handoff pgtype.Text
	err := testPool.QueryRow(context.Background(), `
		SELECT id::text, handoff_note FROM agent_task_queue
		WHERE issue_id = $1 AND rerun_of_task_id = $2
	`, issueID, parkedTaskID).Scan(&id, &handoff)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ""
	}
	if err != nil {
		t.Fatalf("read resume task: %v", err)
	}
	return id, handoff.String
}

// Overdue ask, no overrides, agent lead: the deadline escalates once — the card
// stays open, the parked task stays blocked, one durable comment names the
// built-in default and the lead's rule, and exactly one run is queued for the
// lead whose trigger is that comment. A second round does nothing at all.
func TestAskPlanEscalation_OverdueAskEscalatesOnceAndKeepsCardOpen(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second, // default timeout is 300s
		leadType: "agent",
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1", escalated)
	}
	if status, by := askPlanTaskStatus(t, fx.taskID); status != "blocked" || by != "" {
		t.Fatalf("task status = %q by %q, want blocked and NOT cancelled", status, by)
	}
	var cardStatus string
	var escalatedAt *time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT status, escalated_at FROM agent_question_card WHERE id = $1`, fx.cardID).Scan(&cardStatus, &escalatedAt); err != nil {
		t.Fatalf("read card: %v", err)
	}
	if cardStatus != "open" {
		t.Fatalf("card status = %q, want open (the Owner still has to answer)", cardStatus)
	}
	if escalatedAt == nil {
		t.Fatal("card escalated_at is NULL after the sweep")
	}
	comment := askPlanSystemComment(t, fx.issueID)
	if comment == "" {
		t.Fatal("expected a system comment")
	}
	if !contains(comment, "built-in default") {
		t.Fatalf("comment must name the governing source, got:\n%s", comment)
	}
	if !contains(comment, "stays open") || !contains(comment, "stays parked") {
		t.Fatalf("comment must say nothing was cancelled, got:\n%s", comment)
	}
	if !contains(comment, "multica question-card answer") {
		t.Fatalf("comment must hand the lead the command, got:\n%s", comment)
	}
	if !contains(comment, "Escalated to the project lead") {
		t.Fatalf("comment must name the escalation, got:\n%s", comment)
	}
	if contains(comment, "mention://") {
		t.Fatalf("system comment must carry no mention links:\n%s", comment)
	}
	queued := askPlanQueuedFor(t, fx.issueID)
	if len(queued) != 1 {
		t.Fatalf("queued tasks = %d, want 1 (the lead's run)", len(queued))
	}
	if queued[0][0] == fx.taskID {
		t.Fatal("the parked task must not be the queued run")
	}
	if queued[0][1] == "" {
		t.Fatal("lead run must carry the escalation comment as its trigger")
	}
	if resumeID, _ := askPlanResumeTask(t, fx.issueID, fx.taskID); resumeID != "" {
		t.Fatal("the sweep must not resume the parked run")
	}

	// Second round: already escalated, so it is no longer a candidate.
	if escalated := sweepAskPlanRound(t); escalated != 0 {
		t.Fatalf("second round escalated = %d, want 0", escalated)
	}
	if n := askPlanSystemCommentCount(t, fx.issueID); n != 1 {
		t.Fatalf("system comments = %d, want 1 (escalation happens once)", n)
	}
	if q := askPlanQueuedFor(t, fx.issueID); len(q) != 1 {
		t.Fatalf("queued tasks after the second round = %d, want 1", len(q))
	}
}

// A park inside its timeout is nobody's business: untouched row, open card,
// no comment, no trigger.
func TestAskPlanEscalation_FreshAskUntouched(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  10 * time.Second,
		leadType: "agent",
	})
	if escalated := sweepAskPlanRound(t); escalated != 0 {
		t.Fatalf("escalated = %d, want 0", escalated)
	}
	if status, _ := askPlanTaskStatus(t, fx.taskID); status != "blocked" {
		t.Fatalf("task status = %q, want blocked", status)
	}
	if comment := askPlanSystemComment(t, fx.issueID); comment != "" {
		t.Fatalf("no comment expected, got:\n%s", comment)
	}
	if queued := askPlanQueuedFor(t, fx.issueID); len(queued) != 0 {
		t.Fatalf("queued tasks = %d, want 0", len(queued))
	}
}

// First answer wins at the candidate level: an answered park sits 'blocked'
// forever in this schema, so the live-evidence join must not see it, and a
// direct escalation call must leave it alone too.
func TestAskPlanEscalation_AnsweredParkIsNeverEscalated(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		answered: true,
		leadType: "agent",
	})
	if escalated := sweepAskPlanRound(t); escalated != 0 {
		t.Fatalf("escalated = %d, want 0 (answered parks are not candidates)", escalated)
	}
	// Direct call too: the gate reads the evidence row and refuses.
	done, err := askPlanTaskService().EscalateParkedAskPlanTask(context.Background(), parseUUID(fx.taskID), 300, "built-in default")
	if err != nil {
		t.Fatalf("EscalateParkedAskPlanTask: %v", err)
	}
	if done {
		t.Fatal("answered park must not escalate")
	}
	if status, _ := askPlanTaskStatus(t, fx.taskID); status != "blocked" {
		t.Fatalf("task status = %q, want blocked after refused escalation", status)
	}
	if comment := askPlanSystemComment(t, fx.issueID); comment != "" {
		t.Fatalf("refused escalation must not comment, got:\n%s", comment)
	}
}

// Rung 3, the Owner answers late: after the escalation the card is still open, so
// the Owner's answer records and resumes the ORIGINAL parked run — the run the
// lead never got to.
func TestAskPlanEscalation_OwnerAnswerAfterEscalationResumesTheParkedRun(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		leadType: "agent",
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1", escalated)
	}
	card, resume, err := askPlanTaskService().AnswerQuestionCard(
		context.Background(),
		parseUUID(fx.cardID),
		parseUUID(testWorkspaceID),
		parseUUID(testUserID),
		service.AnsweredByMember,
		json.RawMessage(`[{"selected":[0],"text":""}]`),
	)
	if err != nil {
		t.Fatalf("AnswerQuestionCard after escalation: %v", err)
	}
	if card.Status != "answered" || card.AnsweredByType != service.AnsweredByMember {
		t.Fatalf("card = status %q by-type %q, want answered/member", card.Status, card.AnsweredByType)
	}
	if resume.RerunOfTaskID != parseUUID(fx.taskID) {
		t.Fatal("the resume run must rerun the original parked task")
	}
	if _, note := askPlanResumeTask(t, fx.issueID, fx.taskID); strings.Contains(note, "on the Owner's behalf") {
		t.Fatalf("a member's answer must not be described as the lead's:\n%s", note)
	}
}

// Rung 2: the lead answers on the Owner's behalf. The answer is stored as the
// lead's (never laundered into the Owner's), the original parked run resumes,
// and the handoff note tells that run who decided.
func TestAskPlanEscalation_LeadAnswerResumesTheParkedRunAsTheLead(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		leadType: "agent",
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1", escalated)
	}
	card, resume, err := askPlanTaskService().AnswerQuestionCard(
		context.Background(),
		parseUUID(fx.cardID),
		parseUUID(testWorkspaceID),
		parseUUID(fx.leadAgentID),
		service.AnsweredByAgent,
		json.RawMessage(`[{"selected":[1],"text":""}]`),
	)
	if err != nil {
		t.Fatalf("lead AnswerQuestionCard: %v", err)
	}
	if card.Status != "answered" || card.AnsweredByType != service.AnsweredByAgent {
		t.Fatalf("card = status %q by-type %q, want answered/agent", card.Status, card.AnsweredByType)
	}
	if card.AnsweredBy != parseUUID(fx.leadAgentID) {
		t.Fatal("answered_by must be the lead agent")
	}
	if resume.RerunOfTaskID != parseUUID(fx.taskID) {
		t.Fatal("the lead's answer must resume the original parked run")
	}
	_, note := askPlanResumeTask(t, fx.issueID, fx.taskID)
	if !contains(note, "Integration Test Agent") || !contains(note, "on the Owner's behalf") {
		t.Fatalf("handoff note must name the lead and the Owner's behalf, got:\n%s", note)
	}
	if !contains(note, "Friday") {
		t.Fatalf("handoff note must carry the chosen label, got:\n%s", note)
	}

	// Whoever answers second loses: the Owner's own answer is refused.
	_, _, err = askPlanTaskService().AnswerQuestionCard(
		context.Background(),
		parseUUID(fx.cardID),
		parseUUID(testWorkspaceID),
		parseUUID(testUserID),
		service.AnsweredByMember,
		json.RawMessage(`[{"selected":[0],"text":""}]`),
	)
	if !errors.Is(err, service.ErrQuestionCardClosed) {
		t.Fatalf("second answer: err = %v, want ErrQuestionCardClosed", err)
	}
}

// Resolution order, amendment 1 wired end to end: a project override of 60s
// escalates a 120s park even when the workspace says 86400, and the comment
// names the override as the source that governed — while the task stays parked.
func TestAskPlanEscalation_ProjectOverrideGoverns(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:           120 * time.Second,
		workspaceSettings: `{"ask_plan_timeout_seconds": 86400}`,
		projectSettings:   `{"ask_plan_timeout_seconds": 60}`,
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1 (project override 60s governs)", escalated)
	}
	comment := askPlanSystemComment(t, fx.issueID)
	if !contains(comment, "project override") {
		t.Fatalf("comment must name the project override as the source, got:\n%s", comment)
	}
	if status, _ := askPlanTaskStatus(t, fx.taskID); status != "blocked" {
		t.Fatalf("task status = %q, want blocked", status)
	}
}

// The mirror: same 120s park, workspace default 86400 and no override — the
// park waits, because the workspace value is the effective timeout.
func TestAskPlanEscalation_WorkspaceDefaultSparesThePark(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:           120 * time.Second,
		workspaceSettings: `{"ask_plan_timeout_seconds": 86400}`,
	})
	if escalated := sweepAskPlanRound(t); escalated != 0 {
		t.Fatalf("escalated = %d, want 0 (workspace default 86400s)", escalated)
	}
	if status, _ := askPlanTaskStatus(t, fx.taskID); status != "blocked" {
		t.Fatalf("task status = %q, want blocked", status)
	}
	if comment := askPlanSystemComment(t, fx.issueID); comment != "" {
		t.Fatalf("no comment expected, got:\n%s", comment)
	}
}

// Member lead: an action-required inbox row addressed to them, no task queued
// for anyone, comment names them — and the card still waits, open.
func TestAskPlanEscalation_MemberLeadGetsInboxNotTask(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		leadType: "member",
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1", escalated)
	}
	if n := askPlanInboxCount(t, fx.issueID, testUserID); n != 1 {
		t.Fatalf("member lead inbox items = %d, want 1", n)
	}
	if queued := askPlanQueuedFor(t, fx.issueID); len(queued) != 0 {
		t.Fatalf("queued tasks = %d, want 0 for a member lead", len(queued))
	}
	if comment := askPlanSystemComment(t, fx.issueID); !contains(comment, "Escalated to the project lead") {
		t.Fatalf("comment must record the escalation, got:\n%s", comment)
	}
	var cardStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_question_card WHERE id = $1`, fx.cardID).Scan(&cardStatus); err != nil {
		t.Fatalf("read card: %v", err)
	}
	if cardStatus != "open" {
		t.Fatalf("card status = %q, want open", cardStatus)
	}
}

// Triage: the comment is still delivered, but the agent-lead task is refused by
// the triage guard — OriginDerived, not OriginNamed, so the guard is not
// bypassed. The escalation is still recorded once.
func TestAskPlanEscalation_TriageHoldsTheAgentLeadTask(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		parkAge:  320 * time.Second,
		leadType: "agent",
		triage:   true,
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1 (the park still escalates)", escalated)
	}
	if comment := askPlanSystemComment(t, fx.issueID); comment == "" {
		t.Fatal("comment must be posted even in triage")
	}
	if queued := askPlanQueuedFor(t, fx.issueID); len(queued) != 0 {
		t.Fatalf("queued tasks = %d, want 0 (triage guard holds the lead task)", len(queued))
	}
}

// Overdue plan: it stays in review, the run stays parked, the comment says plan
// and "the Owner's verdict resumes it" — and with no project lead, only the
// comment: nothing invented.
func TestAskPlanEscalation_OverduePlanStaysInReview(t *testing.T) {
	fx := setupAskPlanSweepFixture(t, askPlanSweepOpts{
		plan:    true,
		parkAge: 320 * time.Second,
	})
	if escalated := sweepAskPlanRound(t); escalated != 1 {
		t.Fatalf("escalated = %d, want 1", escalated)
	}
	var planStatus string
	var escalatedAt *time.Time
	if err := testPool.QueryRow(context.Background(), `SELECT status, escalated_at FROM agent_plan WHERE id = $1`, fx.planID).Scan(&planStatus, &escalatedAt); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	if planStatus != "in_review" {
		t.Fatalf("plan status = %q, want in_review (waits for the Owner)", planStatus)
	}
	if escalatedAt == nil {
		t.Fatal("plan escalated_at is NULL after the sweep")
	}
	if status, by := askPlanTaskStatus(t, fx.taskID); status != "blocked" || by != "" {
		t.Fatalf("task status = %q by %q, want blocked and NOT cancelled", status, by)
	}
	comment := askPlanSystemComment(t, fx.issueID)
	if !contains(comment, "plan") || !contains(comment, "No project lead is set") {
		t.Fatalf("comment must say plan + no-lead, got:\n%s", comment)
	}
	if !contains(comment, "the Owner's verdict") {
		t.Fatalf("comment must say the Owner's verdict resumes it, got:\n%s", comment)
	}
	if !contains(comment, "no lead verdict path") {
		t.Fatalf("comment must record the plan-verdict gap, got:\n%s", comment)
	}
	if queued := askPlanQueuedFor(t, fx.issueID); len(queued) != 0 {
		t.Fatalf("queued tasks = %d, want 0", len(queued))
	}
	if n := askPlanInboxCount(t, fx.issueID, testUserID); n != 0 {
		t.Fatalf("inbox items = %d, want 0", n)
	}
	// A second round leaves the escalated plan alone.
	if escalated := sweepAskPlanRound(t); escalated != 0 {
		t.Fatalf("second round escalated = %d, want 0", escalated)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
