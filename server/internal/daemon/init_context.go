package daemon

// Per-run init-context pricing (M-140).
//
// The first request a run sends is assembled from seven parts that come from
// three different places: the runtime brief on disk, the MOCT pre-run memory
// block, and the per-turn task prompt. Only the daemon sees all three at once,
// so the report is built here and forwarded on the terminal callback into
// agent_task_queue.result — the run row is where "why was this run's context
// thin" has to be answerable without a second investigation.

import (
	"encoding/json"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/initctx"
)

// newInitContext prices the first request this run will send.
//
// memoryBlock is the MOCT block that BuildPrompt prepends verbatim to the task
// prompt, so it is subtracted from the prompt's byte count before the task part
// is priced: counting it twice would report a run as larger than it is and make
// the memory part's budget meaningless.
func newInitContext(provider string, brief execenv.RuntimeBrief, memory initctx.PartReport, prompt, memoryBlock string, task Task) *initctx.Report {
	parts := make([]initctx.PartReport, 0, len(brief.Parts)+4)
	parts = append(parts, brief.Parts...)
	parts = append(parts,
		initctx.ToolPart(provider),
		memory,
		initctx.PartReport{
			Part:     initctx.PartTask,
			Tokens:   initctx.EstimateBytes(len(prompt) - len(memoryBlock)),
			Measured: true,
			Note:     "per-turn task prompt from the " + taskSurface(task) + " surface; never trimmed, so a small one is a short trigger",
		},
		initctx.SeedPart(),
	)
	return initctx.Finalize(provider, parts, brief.SHA256)
}

// taskSurface names the prompt shape this run used. It mirrors the surface
// split the brief already makes (execenv's task kinds) at the granularity the
// run row needs: a reader comparing two thin task parts wants to know one was a
// one-line wakeup and the other a fresh assignment.
func taskSurface(task Task) string {
	switch {
	case task.WakeupID != "":
		return "wakeup"
	case task.QuickCreatePrompt != "":
		return "quick-create"
	case task.AutopilotRunID != "":
		return "autopilot"
	case task.TriggerCommentID != "":
		return "trigger-comment"
	case task.IssueID != "":
		return "issue-assignment"
	default:
		return "chat"
	}
}

// marshalInitContext renders the report for the terminal callback. A marshal
// failure drops the report rather than the run: the run row loses one
// observability field, which must never cost the user their result.
func marshalInitContext(report *initctx.Report) json.RawMessage {
	if report == nil {
		return nil
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil
	}
	return raw
}
