package handler

// The plan gate's daemon routes, driven for real: a task is parked through the
// ask-plan submit path, the parked plan is read and verdict'd with a daemon
// token, and the effect is read back out of the database. These are the only
// tests that exercise GET /tasks/{id}/parked-plan and POST
// /tasks/{id}/plan-verdict as requests — the daemon-side tests use a fake
// server, which cannot catch a change to what the routes actually return.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// daemonPlanGateRequest authenticates the way a real daemon does: an mdt_ token
// bound to the fixture runtime's daemon_id and owner (MUL-8154), with the task
// id in the path.
func daemonPlanGateRequest(t *testing.T, f askPlanFixture, method, suffix string, body any) *http.Request {
	t.Helper()
	return daemonPlanGateRequestAs(t, f, "park-daemon", method, suffix, body)
}

// daemonPlanGateRequestAs uses a named daemon: a daemon token only reaches
// runtimes carrying its own daemon_id (MUL-8154), which also keeps two fixtures
// in one test from colliding on the runtime's (workspace, daemon, provider)
// key.
func daemonPlanGateRequestAs(t *testing.T, f askPlanFixture, daemonID, method, suffix string, body any) *http.Request {
	t.Helper()
	req := newDaemonTokenRequest(method, "/api/daemon/tasks/"+f.taskID+suffix, body, testWorkspaceID, daemonID)
	return withURLParams(req, "taskId", f.taskID)
}

// parkPlanThroughAskPlan drives the real agent path up to the park: create the
// plan, write its body, submit it. Submitting is what sets in_review, parks the
// task and stamps submitted_revision, so nothing here fabricates a plan row.
func parkPlanThroughAskPlan(t *testing.T, f askPlanFixture, title, markdown string) planResponse {
	t.Helper()
	created := createPlanForTask(t, f, title, []string{"Approach"})

	writeReq := taskTokenRequestWithoutPathTask(t, f, http.MethodPut,
		"/api/workspaces/"+testWorkspaceID+"/plans/"+created.ID+"/sections",
		map[string]any{
			"base_revision": created.Revision,
			"section":       map[string]any{"section_id": "s1", "heading": "Approach", "markdown": markdown},
		})
	writeReq = withURLParams(writeReq, "planId", created.ID)
	var written planResponse
	testutil.Call(t, testHandler.WritePlanSection, writeReq).Want(http.StatusOK).JSON(&written)

	submitReq := taskTokenRequestWithoutPathTask(t, f, http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/plans/"+created.ID+"/submit",
		map[string]any{"revision": written.Revision})
	submitReq = withURLParams(submitReq, "planId", created.ID)
	var submitted planResponse
	testutil.Call(t, testHandler.SubmitPlan, submitReq).Want(http.StatusAccepted).JSON(&submitted)
	return submitted
}

// verdict posts a gate verdict and returns its status and decoded body.
func postGateVerdict(t *testing.T, f askPlanFixture, body map[string]any) (int, map[string]any) {
	t.Helper()
	w := testutil.Call(t, testHandler.SubmitParkedPlanVerdict,
		daemonPlanGateRequest(t, f, http.MethodPost, "/plan-verdict", body))
	var out map[string]any
	if len(w.Body.Bytes()) > 0 {
		out = w.Map()
	}
	return w.Code, out
}

// TestDaemonParkedPlan_RealFlowReadsAndApproves walks the whole loop once:
// park, read with the daemon's token, approve, and check the database moved.
func TestDaemonParkedPlan_RealFlowReadsAndApproves(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Release plan", "Ship the gate.")

	var parked daemonParkedPlanResponse
	testutil.Call(t, testHandler.GetParkedPlan,
		daemonPlanGateRequest(t, f, http.MethodGet, "/parked-plan", nil)).
		Want(http.StatusOK).JSON(&parked)

	if parked.Status != "ready" {
		t.Fatalf("parked plan status = %q, want ready", parked.Status)
	}
	if parked.PlanID != plan.ID {
		t.Fatalf("plan_id = %q, want %q", parked.PlanID, plan.ID)
	}
	// submitted_revision is the fence ReviewPlan will apply; the gate must send
	// exactly this back.
	if parked.Revision != plan.Revision || parked.SubmittedRevision != plan.Revision {
		t.Fatalf("revisions = %d/%d, want both %d", parked.Revision, parked.SubmittedRevision, plan.Revision)
	}
	if !strings.Contains(parked.Markdown, "# Release plan") || !strings.Contains(parked.Markdown, "Ship the gate.") {
		t.Fatalf("markdown is not the submitted plan: %q", parked.Markdown)
	}

	status, body := postGateVerdict(t, f, map[string]any{
		"plan_id":       parked.PlanID,
		"base_revision": parked.SubmittedRevision,
		"verdict":       "approve",
	})
	if status != http.StatusOK {
		t.Fatalf("verdict status = %d body = %v", status, body)
	}
	resumeTask, _ := body["resume_task"].(string)
	if resumeTask == "" {
		t.Fatalf("approve returned no resume_task: %v", body)
	}

	var planStatus string
	dbfx.QueryRow(t, `SELECT status FROM agent_plan WHERE id=$1`, plan.ID).Scan(&planStatus)
	if planStatus != "approved" {
		t.Fatalf("plan status = %q, want approved", planStatus)
	}
	// reviewed_by is NOT NULL: the review names the credential's user (the
	// runtime's owner, here the fixture user), and the note says the gate made
	// the call so it stays distinguishable from a panel verdict.
	var reviewedBy, note string
	dbfx.QueryRow(t, `SELECT reviewed_by::text, note FROM agent_plan_review WHERE plan_id=$1 AND verdict='approve'`, plan.ID).
		Scan(&reviewedBy, &note)
	if reviewedBy != testUserID {
		t.Fatalf("reviewed_by = %q, want the daemon owner %q", reviewedBy, testUserID)
	}
	if !strings.Contains(note, "Plannotator gate") {
		t.Fatalf("note = %q, want the gate named", note)
	}

	// The parked run's resume is queued for the same runtime and agent, which is
	// what "the task returns to the daemon" means: the claim loop can pick it up.
	var resumeRuntime, resumeAgent, resumeIssue, resumeStatus, evidence string
	var rerunOf string
	dbfx.QueryRow(t, `SELECT runtime_id::text, agent_id::text, issue_id::text, status, trigger_evidence_kind, rerun_of_task_id::text
		FROM agent_task_queue WHERE id=$1`, resumeTask).
		Scan(&resumeRuntime, &resumeAgent, &resumeIssue, &resumeStatus, &evidence, &rerunOf)
	if resumeRuntime != f.runtimeID || resumeAgent != f.agentID || resumeIssue != f.issueID {
		t.Fatalf("resume task = runtime %s agent %s issue %s, want %s/%s/%s",
			resumeRuntime, resumeAgent, resumeIssue, f.runtimeID, f.agentID, f.issueID)
	}
	if resumeStatus != "queued" || evidence != "plan_verdict" || rerunOf != f.taskID {
		t.Fatalf("resume task = status %s evidence %s rerun_of %s", resumeStatus, evidence, rerunOf)
	}

	// The plan is no longer open, so a second read finds nothing to review.
	var again daemonParkedPlanResponse
	testutil.Call(t, testHandler.GetParkedPlan,
		daemonPlanGateRequest(t, f, http.MethodGet, "/parked-plan", nil)).
		Want(http.StatusOK).JSON(&again)
	if again.Status != "none" {
		t.Fatalf("parked plan after approval = %+v, want status none", again)
	}
}

// TestDaemonPlanVerdict_ChangesRequestedCarriesFeedback: the annotator's text
// becomes the review's comments, and the plan goes back to the agent.
func TestDaemonPlanVerdict_ChangesRequestedCarriesFeedback(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Risky plan", "Do it all at once.")

	status, body := postGateVerdict(t, f, map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision,
		"verdict":       "changes_requested",
		"feedback":      "split the migration",
	})
	if status != http.StatusOK {
		t.Fatalf("verdict status = %d body = %v", status, body)
	}
	if resume, _ := body["resume_task"].(string); resume == "" {
		t.Fatalf("changes_requested returned no resume task: %v", body)
	}

	var planStatus, comments string
	dbfx.QueryRow(t, `SELECT status FROM agent_plan WHERE id=$1`, plan.ID).Scan(&planStatus)
	if planStatus != "changes_requested" {
		t.Fatalf("plan status = %q, want changes_requested", planStatus)
	}
	dbfx.QueryRow(t, `SELECT comments::text FROM agent_plan_review WHERE plan_id=$1 AND verdict='changes_requested'`, plan.ID).
		Scan(&comments)
	if !strings.Contains(comments, "split the migration") {
		t.Fatalf("review comments = %s, want the annotator's feedback", comments)
	}
}

// TestDaemonPlanVerdict_ChangesRequestedWithoutFeedbackRefused: an annotation
// with no text is not a review the agent can act on, and must not silently
// change the plan's state.
func TestDaemonPlanVerdict_ChangesRequestedWithoutFeedbackRefused(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Plan", "Body.")

	status, body := postGateVerdict(t, f, map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision,
		"verdict":       "changes_requested",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d body = %v, want 400", status, body)
	}
	if body["code"] != "review_needs_comment" {
		t.Fatalf("code = %v, want review_needs_comment", body["code"])
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_plan WHERE id=$1 AND status='in_review'`, plan.ID); n != 1 {
		t.Fatalf("plan left in_review after a refused verdict")
	}
}

// TestDaemonPlanVerdict_StaleRevisionChangesNothing: the gate reviewed an older
// revision, so its verdict must not land on the current one, and no run may be
// queued from it.
func TestDaemonPlanVerdict_StaleRevisionChangesNothing(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Plan", "Body.")

	status, body := postGateVerdict(t, f, map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision - 1,
		"verdict":       "approve",
	})
	if status != http.StatusConflict {
		t.Fatalf("status = %d body = %v, want 409", status, body)
	}
	if body["error"] != "stale_plan_revision" {
		t.Fatalf("error = %v, want stale_plan_revision", body["error"])
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_plan WHERE id=$1 AND status='in_review'`, plan.ID); n != 1 {
		t.Fatalf("a stale verdict changed the plan")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, f.taskID); n != 0 {
		t.Fatalf("a stale verdict queued %d resume runs", n)
	}
}

// TestDaemonPlanVerdict_SecondVerdictConflicts: the first verdict wins, and the
// loser changes nothing and queues nothing.
func TestDaemonPlanVerdict_SecondVerdictConflicts(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Plan", "Body.")
	verdict := map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision,
		"verdict":       "approve",
	}
	if status, body := postGateVerdict(t, f, verdict); status != http.StatusOK {
		t.Fatalf("first verdict status = %d body = %v", status, body)
	}
	status, body := postGateVerdict(t, f, verdict)
	if status != http.StatusConflict {
		t.Fatalf("second verdict status = %d body = %v, want 409", status, body)
	}
	if body["code"] != "plan_not_in_review" {
		t.Fatalf("code = %v, want plan_not_in_review", body["code"])
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, f.taskID); n != 1 {
		t.Fatalf("resume runs = %d, want exactly 1", n)
	}
}

// TestDaemonPlanVerdict_PlanIDFromAnotherTaskRefused: the route resolves the
// plan from the task, so a plan id belonging to a different run cannot be
// verdict'd through this task.
func TestDaemonPlanVerdict_PlanIDFromAnotherTaskRefused(t *testing.T) {
	first := newAskPlanFixture(t)
	second := newAskPlanFixtureWithDaemon(t, "gate-daemon-2", "Second Plan Agent")
	firstPlan := parkPlanThroughAskPlan(t, first, "First plan", "First body.")
	secondPlan := parkPlanThroughAskPlan(t, second, "Second plan", "Second body.")

	// First task's daemon names the second task's plan.
	w := testutil.Call(t, testHandler.SubmitParkedPlanVerdict,
		daemonPlanGateRequestAs(t, first, "park-daemon", http.MethodPost, "/plan-verdict", map[string]any{
			"plan_id":       secondPlan.ID,
			"base_revision": secondPlan.Revision,
			"verdict":       "approve",
		}))
	status := w.Code
	body := w.Map()
	if status != http.StatusConflict {
		t.Fatalf("status = %d body = %v, want 409", status, body)
	}
	// Refused before ReviewPlan: the named plan is not the open plan of THIS
	// task's issue, so the gate's feedback could only land on text it never saw.
	if body["code"] != "stale_plan_revision" {
		t.Fatalf("code = %v, want stale_plan_revision", body["code"])
	}
	for _, plan := range []planResponse{firstPlan, secondPlan} {
		if n := dbfx.Count(t, `SELECT count(*) FROM agent_plan WHERE id=$1 AND status='in_review'`, plan.ID); n != 1 {
			t.Fatalf("plan %s changed after a cross-task verdict", plan.ID)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id IN ($1, $2)`, first.taskID, second.taskID); n != 0 {
		t.Fatalf("a cross-task verdict queued %d resume runs", n)
	}
}

// TestDaemonParkedPlan_NoOpenPlan: an ask park has no plan, and the gate must
// find nothing to review rather than an empty document.
func TestDaemonParkedPlan_NoOpenPlan(t *testing.T) {
	f := newAskPlanFixture(t)

	var parked daemonParkedPlanResponse
	testutil.Call(t, testHandler.GetParkedPlan,
		daemonPlanGateRequest(t, f, http.MethodGet, "/parked-plan", nil)).
		Want(http.StatusOK).JSON(&parked)
	if parked.Status != "none" || parked.Markdown != "" {
		t.Fatalf("parked plan = %+v, want status none and no body", parked)
	}

	status, body := postGateVerdict(t, f, map[string]any{
		"plan_id":       "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001",
		"base_revision": 1,
		"verdict":       "approve",
	})
	if status != http.StatusConflict || body["code"] != "plan_not_in_review" {
		t.Fatalf("verdict without a plan = %d %v, want 409 plan_not_in_review", status, body)
	}
}

// TestDaemonParkedPlan_CrossWorkspaceTokenNotFound: the daemon token's
// workspace is the fence. A task in this workspace is invisible to a token
// bound to another one.
func TestDaemonParkedPlan_CrossWorkspaceTokenNotFound(t *testing.T) {
	f := newAskPlanFixture(t)
	parkPlanThroughAskPlan(t, f, "Plan", "Body.")

	req := newDaemonTokenRequest(http.MethodGet, "/api/daemon/tasks/"+f.taskID+"/parked-plan", nil,
		"00000000-0000-0000-0000-000000000000", "park-daemon")
	req = withURLParams(req, "taskId", f.taskID)
	testutil.Call(t, testHandler.GetParkedPlan, req).Want(http.StatusNotFound)
}

// TestDaemonParkedPlan_CapsALongPlan: a plan can hold 40 sections of 20k, which
// is larger than the cap. The read must stop at the cap and still hand the
// annotator valid UTF-8.
func TestDaemonParkedPlan_CapsALongPlan(t *testing.T) {
	f := newAskPlanFixture(t)
	outline := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		outline = append(outline, "Section "+strings.Repeat("x", i%5))
	}
	created := createPlanForTask(t, f, "Huge plan", outline)
	revision := created.Revision
	body := strings.Repeat("a", planSectionCharMax)
	for i := 1; i <= len(outline); i++ {
		writeReq := taskTokenRequestWithoutPathTask(t, f, http.MethodPut,
			"/api/workspaces/"+testWorkspaceID+"/plans/"+created.ID+"/sections",
			map[string]any{
				"base_revision": revision,
				"section": map[string]any{
					"section_id": "s" + strconv.Itoa(i), "heading": "Section", "markdown": body,
				},
			})
		writeReq = withURLParams(writeReq, "planId", created.ID)
		var written planResponse
		testutil.Call(t, testHandler.WritePlanSection, writeReq).Want(http.StatusOK).JSON(&written)
		revision = written.Revision
	}
	submitReq := taskTokenRequestWithoutPathTask(t, f, http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/plans/"+created.ID+"/submit",
		map[string]any{"revision": revision})
	submitReq = withURLParams(submitReq, "planId", created.ID)
	testutil.Call(t, testHandler.SubmitPlan, submitReq).Want(http.StatusAccepted)

	var parked daemonParkedPlanResponse
	testutil.Call(t, testHandler.GetParkedPlan,
		daemonPlanGateRequest(t, f, http.MethodGet, "/parked-plan", nil)).
		Want(http.StatusOK).JSON(&parked)
	if parked.Status != "ready" {
		t.Fatalf("status = %q, want ready", parked.Status)
	}
	if len(parked.Markdown) > daemonParkedPlanMaxBytes {
		t.Fatalf("markdown is %d bytes, cap is %d", len(parked.Markdown), daemonParkedPlanMaxBytes)
	}
	if !utf8.ValidString(parked.Markdown) {
		t.Fatalf("the capped plan is not valid UTF-8")
	}
	// The plan really was larger than the cap: 20 sections of the maximum body.
	if len(body)*len(outline) <= daemonParkedPlanMaxBytes {
		t.Fatalf("fixture plan is not over the cap")
	}
}

// TestDaemonPlanVerdict_MemberCredentialRecordsThatMember: DaemonAuth accepts a
// member credential on these routes, and a verdict posted that way must be
// recorded against that member. A state-only assertion would miss it — the
// whole point of reviewed_by is the identity — so this reads the review row.
//
// The member also owns the runtime: the route's binding check refuses a member
// verdict for a runtime someone else owns, and using a second member keeps the
// recorded id from matching the suite's default user by accident.
func TestDaemonPlanVerdict_MemberCredentialRecordsThatMember(t *testing.T) {
	f := newAskPlanFixture(t)
	plan := parkPlanThroughAskPlan(t, f, "Member plan", "Ship it.")

	member := dbfx.User(t, "Gate Member", "gate-member-"+f.taskID+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, member, "member")
	dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2 WHERE id=$1`, f.runtimeID, member)

	req := newRequestAs(member, http.MethodPost, "/api/daemon/tasks/"+f.taskID+"/plan-verdict", map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision,
		"verdict":       "approve",
	})
	req = withURLParams(req, "taskId", f.taskID)
	testutil.Call(t, testHandler.SubmitParkedPlanVerdict, req).Want(http.StatusOK)

	var reviewedBy, note string
	dbfx.QueryRow(t, `SELECT reviewed_by::text, note FROM agent_plan_review WHERE plan_id=$1 AND verdict='approve'`, plan.ID).
		Scan(&reviewedBy, &note)
	if reviewedBy != member {
		t.Fatalf("reviewed_by = %q, want the posting member %q", reviewedBy, member)
	}
	// The note is what still separates a gated approval from a panel decision;
	// recording the member must not turn this into an ordinary review.
	if !strings.Contains(note, "Plannotator gate") {
		t.Fatalf("note = %q, want the gate named", note)
	}
	var planStatus string
	dbfx.QueryRow(t, `SELECT status FROM agent_plan WHERE id=$1`, plan.ID).Scan(&planStatus)
	if planStatus != "approved" {
		t.Fatalf("plan status = %q, want approved", planStatus)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, f.taskID); n != 1 {
		t.Fatalf("resume runs = %d, want exactly 1", n)
	}
}

// TestDaemonPlanVerdict_OwnerlessTokenRefused: a Qoder-managed daemon token has
// no owner — daemon_token.owner_id stays NULL for the server-run bridge, and
// DaemonAuth deliberately does not fall back to a member for that path. There is
// therefore nobody to record a verdict against, and the route must refuse rather
// than invent a reviewer or 500. The plan stays parked for a human.
//
// This is the only way an ownerless credential reaches the verdict route: for
// every other daemon token the runtime binding check refuses it first, so the
// test goes through the real DaemonAuth with a real token row and a live
// cloud_agent_connection, which is what lets a qoder-managed token past binding.
func TestDaemonPlanVerdict_OwnerlessTokenRefused(t *testing.T) {
	ctx := context.Background()
	daemonID := "qoder-managed-" + testWorkspaceID
	f := newAskPlanFixtureWithDaemon(t, daemonID, "Qoder Gate Agent")
	plan := parkPlanThroughAskPlan(t, f, "Bridge plan", "Ship it.")

	raw, err := auth.GenerateDaemonToken()
	if err != nil {
		t.Fatal(err)
	}
	token, err := testHandler.Queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(raw),
		WorkspaceID: parseUUID(testWorkspaceID),
		DaemonID:    daemonID,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("create daemon token: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM daemon_token WHERE id=$1`, token.ID) })
	dbfx.Exec(t, `INSERT INTO cloud_agent_connection (workspace_id, provider, owner_id, token_id, config_encrypted, enabled)
		VALUES ($1, 'qoder_cloud', $2, $3, 'test-only', true)`, testWorkspaceID, testUserID, token.ID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM cloud_agent_connection WHERE provider='qoder_cloud' AND token_id=$1`, token.ID)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/daemon/tasks/"+f.taskID+"/plan-verdict", strings.NewReader(mustJSON(t, map[string]any{
		"plan_id":       plan.ID,
		"base_revision": plan.Revision,
		"verdict":       "approve",
	})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	req = withURLParams(req, "taskId", f.taskID)

	rec := httptest.NewRecorder()
	middleware.DaemonAuth(testHandler.Queries, nil, nil, nil)(
		http.HandlerFunc(testHandler.SubmitParkedPlanVerdict)).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s, want 403", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reviewer identity") {
		t.Fatalf("body = %s, want the reviewer-identity refusal", rec.Body.String())
	}

	// The verdict changed nothing: the plan is still awaiting a human, and no
	// review or resume run was recorded from the refused call.
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_plan WHERE id=$1 AND status='in_review'`, plan.ID); n != 1 {
		t.Fatalf("plan left in_review after an ownerless verdict")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_plan_review WHERE plan_id=$1`, plan.ID); n != 0 {
		t.Fatalf("an ownerless verdict recorded %d reviews", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE rerun_of_task_id=$1`, f.taskID); n != 0 {
		t.Fatalf("an ownerless verdict queued %d resume runs", n)
	}
}
