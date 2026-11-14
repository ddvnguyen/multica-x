package handler

// M-348: the plan page's revision label and read-old-revision endpoint. The
// write-time base_revision is persisted on the comment, and an older revision
// reads back per section from the newest ledger version at or before it.

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestPlanCommentRevisionAndReadOldRevision(t *testing.T) {
	f := newAskPlanFixture(t)
	planID, rev := submittedPlan(t, f)
	t.Cleanup(func() {
		dbfx.Exec(t, `DELETE FROM agent_plan_comment WHERE plan_id=$1`, planID)
		dbfx.Exec(t, `DELETE FROM agent_plan_section_version WHERE plan_id=$1`, planID)
	})

	// A comment made at the submitted revision carries that revision label.
	var first planCommentBody
	testutil.Call(t, testHandler.CreatePlanComment,
		withURLParams(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/comments", map[string]any{
			"base_revision": rev,
			"section_id":    "s1",
			"quote":         "Ship on Thursday",
			"body":          "Be specific about the day.",
		}), "id", testWorkspaceID, "planId", planID),
	).Want(http.StatusCreated).JSON(&first)
	if first.Comment.Revision == nil || *first.Comment.Revision != rev {
		t.Fatalf("comment revision = %+v, want %d", first.Comment.Revision, rev)
	}

	// A human edit bumps the plan to rev+1 with new text.
	var edited planResponse
	testutil.Call(t, testHandler.UpdatePlanSection,
		withURLParams(newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/sections/s1", map[string]any{
			"base_revision": rev,
			"heading":       "Goal",
			"markdown":      "Ship on Thursday, z first.",
		}), "id", testWorkspaceID, "planId", planID, "sectionId", "s1"),
	).Want(http.StatusOK).JSON(&edited)
	if edited.Revision != rev+1 {
		t.Fatalf("edit revision = %d, want %d", edited.Revision, rev+1)
	}

	// Reading the older revision shows the older text, read-only metadata
	// naming what is current.
	var old struct {
		PlanID   string `json:"plan_id"`
		Revision int32  `json:"revision"`
		Current  int32  `json:"current"`
		Sections []struct {
			SectionID string `json:"section_id"`
			Heading   string `json:"heading"`
			Markdown  string `json:"markdown"`
			Available bool   `json:"available"`
		} `json:"sections"`
	}
	testutil.Call(t, testHandler.GetPlanRevision,
		withURLParams(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/revisions/"+strconv.FormatInt(int64(rev), 10), nil),
			"id", testWorkspaceID, "planId", planID, "revision", strconv.FormatInt(int64(rev), 10)),
	).Want(http.StatusOK).JSON(&old)
	if old.Revision != rev || old.Current != rev+1 || old.PlanID != planID {
		t.Fatalf("old revision read: %+v", old)
	}
	if len(old.Sections) != 1 || !old.Sections[0].Available || old.Sections[0].Markdown != "Ship on Thursday" {
		t.Fatalf("old revision sections: %+v", old.Sections)
	}

	// Revisions below 1 and past the current one are 404, never invented text
	// and never the current write path.
	testutil.Call(t, testHandler.GetPlanRevision,
		withURLParams(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/revisions/0", nil),
			"id", testWorkspaceID, "planId", planID, "revision", "0"),
	).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.GetPlanRevision,
		withURLParams(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/plans/"+planID+"/revisions/99", nil),
			"id", testWorkspaceID, "planId", planID, "revision", "99"),
	).Want(http.StatusNotFound)

	// A plan in another workspace is not this workspace's plan to read.
	testutil.Call(t, testHandler.GetPlanRevision,
		withURLParams(newRequest(http.MethodGet, "/api/workspaces/00000000-0000-0000-0000-000000000000/plans/"+planID+"/revisions/"+strconv.FormatInt(int64(rev), 10), nil),
			"id", "00000000-0000-0000-0000-000000000000", "planId", planID, "revision", strconv.FormatInt(int64(rev), 10)),
	).Want(http.StatusNotFound)

	// The issue read carries the revision label with the thread.
	var state askPlanStateResponse
	testutil.Call(t, testHandler.GetIssueAskPlan, issueAskPlanRequest(t, f)).Want(http.StatusOK).JSON(&state)
	if state.Plan == nil || len(state.Plan.Comments) != 1 {
		t.Fatalf("ask-plan read comments: %+v", state.Plan)
	}
	if state.Plan.Comments[0].Revision == nil || *state.Plan.Comments[0].Revision != rev {
		t.Fatalf("thread revision label: %+v", state.Plan.Comments[0])
	}
}
