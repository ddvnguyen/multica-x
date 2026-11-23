package daemon

// Client half of the plan gate (docs/plannotator-gate.md): the two calls the
// daemon makes around a gate run. Both are task-scoped — the server resolves
// the plan from the task — so nothing here can name another plan.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ParkedPlan is GET /api/daemon/tasks/{taskId}/parked-plan's answer. Status is
// "ready" with Markdown filled, or "none" when the task has no submitted plan
// awaiting a verdict.
type ParkedPlan struct {
	Status            string `json:"status"`
	PlanID            string `json:"plan_id"`
	Title             string `json:"title"`
	Revision          int32  `json:"revision"`
	SubmittedRevision int32  `json:"submitted_revision"`
	Markdown          string `json:"markdown"`
}

// Ready reports whether there is a plan body to review.
func (p *ParkedPlan) Ready() bool {
	return p != nil && p.Status == "ready" && p.Markdown != ""
}

// BaseRevision is the revision a verdict has to be fenced on. ReviewPlan
// compares against submitted_revision — the revision the plan was submitted
// at — not against the live edit counter. They agree for an in_review plan
// today (an in-review edit advances both), but the fence belongs to the
// submit, so send the field the server checks rather than the one that happens
// to match it.
func (p *ParkedPlan) BaseRevision() int32 {
	if p == nil {
		return 0
	}
	if p.SubmittedRevision > 0 {
		return p.SubmittedRevision
	}
	return p.Revision
}

// ParkedPlanVerdict is the body of the gate's verdict. Verdict uses the plan
// vocabulary (approve | changes_requested | cancel); Feedback is the
// annotator's Markdown and becomes the review's comments.
type ParkedPlanVerdict struct {
	PlanID       string
	BaseRevision int32
	Verdict      string
	Feedback     string
}

// GetParkedPlan reads the open plan of the task's issue.
func (c *Client) GetParkedPlan(ctx context.Context, taskID string) (*ParkedPlan, error) {
	var out ParkedPlan
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/parked-plan", taskID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SubmitParkedPlanVerdict applies the gate's verdict and returns the resume
// task id ("" when the server recorded the verdict without queueing one).
func (c *Client) SubmitParkedPlanVerdict(ctx context.Context, taskID string, v ParkedPlanVerdict) (string, error) {
	body := map[string]any{
		"plan_id":       v.PlanID,
		"base_revision": v.BaseRevision,
		"verdict":       v.Verdict,
	}
	if v.Feedback != "" {
		body["feedback"] = v.Feedback
	}
	var out struct {
		ResumeTask string `json:"resume_task"`
	}
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/plan-verdict", taskID), body, &out); err != nil {
		return "", err
	}
	return out.ResumeTask, nil
}

// planGateConflictCode reports the machine-readable conflict code of a rejected
// verdict, or "" when the failure was not a conflict. The daemon treats a
// conflict as "someone else already decided" — the panel winning the race is a
// normal outcome, not a fault — so it must not retry or warn loudly.
func planGateConflictCode(err error) string {
	var reqErr *requestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusConflict {
		return ""
	}
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(reqErr.Body), &body) != nil {
		return "conflict"
	}
	if body.Error == "" {
		return "conflict"
	}
	return body.Error
}
