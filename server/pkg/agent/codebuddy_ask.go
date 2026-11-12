package agent

import (
	"context"
	"encoding/json"
	"sync"
)

// Structured questions for the CodeBuddy family (codebuddy, freebuff).
//
// CodeBuddy ships an AskUserQuestion tool that asks the user to choose between
// options. Under the daemon's headless stream-json transport there is nobody to
// render it, so the tool used to be disallowed outright (GitHub #6012): the
// call reached the permission bridge, the bridge auto-allowed it, the tool then
// blocked on an answer nobody could give, and the run hung.
//
// The CLI only exposes the tool at all when the SDK side declares the
// capability during an `initialize` control_request
// (SdkHooksManager.supportsStreamJsonAskUserQuestion), so enabling it is a
// two-part contract:
//
//  1. writeCodebuddyInitialize declares capabilities.askUserQuestion before the
//     prompt is written;
//  2. the can_use_tool permission request for AskUserQuestion is held instead
//     of auto-allowed, and answered with `updatedInput.answers`.
//
// Step 2 is the only way to answer. The CLI's permission layer rejects an
// allow that carries no answers ("stream-json allowed AskUserQuestion without
// answers; ignoring"), and the tool's own answer path then reads
// `input.answers` before it ever waits — so the answer must ride the
// permission response, not a follow-up message.

const (
	// codebuddyAskToolName is the provider's tool name. It is matched exactly;
	// CodeBuddy's permission rules do not accept namespaced or prefixed forms.
	codebuddyAskToolName = "AskUserQuestion"

	// codebuddyInitializeRequestID identifies our capability handshake in the
	// CLI's logs and in the control_response it sends back.
	codebuddyInitializeRequestID = "multica-initialize"

	// codebuddySkipQuestionAction is the provider's own "the user declined"
	// action. The tool turns it into a successful result telling the model to
	// continue with what it already has — the outcome we want on expiry, and
	// strictly better than a permission denial, which surfaces as a tool error.
	codebuddySkipQuestionAction = "skip_question"
)

// codebuddyStdin serializes writes to the CLI's stdin.
//
// Control responses are written from the read loop (auto-approvals) and from
// the goroutine holding an ask (its answer), and the prompt is written from a
// third goroutine. Line writes that interleave would corrupt the stream, so
// every writer goes through here.
type codebuddyStdin struct {
	mu sync.Mutex
	w  interface{ Write([]byte) (int, error) }
}

// writeLine marshals v and writes it as one newline-terminated frame.
func (s *codebuddyStdin) writeLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.w.Write(data)
	return err
}

// writeCodebuddyInitialize declares the SDK capabilities this bridge supports.
//
// hasPrompt tells the CLI a user prompt follows on stdin, which is what keeps
// it from treating the initialize frame as the whole session and exiting after
// replaying a resume.
func writeCodebuddyInitialize(stdin *codebuddyStdin) error {
	return stdin.writeLine(map[string]any{
		"type":       "control_request",
		"request_id": codebuddyInitializeRequestID,
		"request": map[string]any{
			"subtype":   "initialize",
			"hasPrompt": true,
			"capabilities": map[string]any{
				// Without this the CLI reports
				// "stream-json peer did not declare askUserQuestion
				// capability" and the tool never reaches the model.
				"askUserQuestion": true,
			},
		},
	})
}

// codebuddyAskInput mirrors the tool's input. Only the fields needed to build a
// question record are modelled; the raw map is what gets echoed back, so an
// unmodelled field survives the round trip.
type codebuddyAskInput struct {
	Questions []codebuddyAskQuestion `json:"questions"`
}

type codebuddyAskQuestion struct {
	Question    string               `json:"question"`
	Header      string               `json:"header"`
	Options     []codebuddyAskOption `json:"options"`
	MultiSelect bool                 `json:"multiSelect"`
}

type codebuddyAskOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// handleAskControlRequest parks one ask tool call until the daemon answers it.
//
// It runs on its own goroutine: the caller is the stdout read loop, and
// blocking there would stop draining the CLI's output for as long as the
// question is open — the CLI would then block writing into a full pipe and the
// run would deadlock with the question still unanswered.
func (b *codebuddyBackend) handleAskControlRequest(runCtx context.Context, requestID string, req codebuddyControlRequestPayload, stdin *codebuddyStdin, ask Asker) {
	var raw map[string]any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &raw); err != nil {
			raw = nil
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	var input codebuddyAskInput
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &input)
	}
	if len(input.Questions) == 0 {
		// A malformed call cannot be turned into a question record, and
		// allowing it without answers makes the CLI reject the permission
		// answer outright, so deny it with a reason the model can act on.
		b.denyControlRequest(stdin, requestID, "AskUserQuestion was called without any questions; continue with your best assumption or end the turn.")
		return
	}

	askReq := AskRequest{ToolCallID: req.ToolUseID}
	for _, q := range input.Questions {
		options := make([]AskOption, 0, len(q.Options))
		for _, opt := range q.Options {
			options = append(options, AskOption{Label: opt.Label, Description: opt.Description})
		}
		askReq.Questions = append(askReq.Questions, AskQuestion{
			Header:      q.Header,
			Prompt:      q.Question,
			Options:     options,
			MultiSelect: q.MultiSelect,
		})
	}

	answer, err := ask.Ask(runCtx, askReq)
	if runCtx.Err() != nil {
		// The run is over; the CLI is being torn down and a write here would
		// race the closed pipe. Nothing is left to unblock.
		return
	}
	if err != nil {
		b.cfg.Logger.Warn("codebuddy: ask failed", "tool_call_id", req.ToolUseID, "error", err)
	}
	if err != nil || answer.Declined {
		reason := answer.Reason
		if reason == "" {
			reason = "no answer was available"
		}
		b.cfg.Logger.Info("codebuddy: ask declined", "tool_call_id", req.ToolUseID, "reason", reason)
		// Skipping is an allow with the provider's own decline action: the
		// tool returns a successful result instructing the model to continue
		// on the information it already has, which is what keeps a run from
		// being lost to an unanswered question.
		raw["answers"] = map[string]any{"_action": codebuddySkipQuestionAction}
		b.allowControlRequest(stdin, requestID, raw)
		return
	}

	answers := make(map[string]any, len(input.Questions))
	for _, q := range input.Questions {
		if value, ok := answer.Answers[q.Question]; ok && value != "" {
			answers[q.Question] = value
		}
	}
	if len(answers) == 0 {
		// The Asker said it answered but named no question we asked; treating
		// that as an answer would send the model a blank result.
		b.cfg.Logger.Warn("codebuddy: ask returned no usable answers", "tool_call_id", req.ToolUseID)
		raw["answers"] = map[string]any{"_action": codebuddySkipQuestionAction}
		b.allowControlRequest(stdin, requestID, raw)
		return
	}
	raw["answers"] = answers
	b.allowControlRequest(stdin, requestID, raw)
}

// allowControlRequest answers a permission request with the tool input to use.
func (b *codebuddyBackend) allowControlRequest(stdin *codebuddyStdin, requestID string, input map[string]any) {
	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				// CodeBuddy's SdkPermissionClient reads `allowed` and treats a
				// missing key as a denial; `behavior` is Claude Code's spelling,
				// which the fork still honours on its other permission paths.
				// Send both so an approval is never read as a silent reject.
				"allowed":      true,
				"behavior":     "allow",
				"updatedInput": input,
			},
		},
	}
	if err := stdin.writeLine(response); err != nil {
		b.cfg.Logger.Warn("codebuddy: failed to write control response", "error", err)
	}
}

// denyControlRequest refuses a permission request. The reason is shown to the
// model, so it carries an instruction rather than a status code.
//
// It must not begin with one of the phrases CodeBuddy treats as a synthetic
// deny ("cancelled", "timeout waiting for user response", "prompt ended",
// "this is an unattended automated session"): those are swallowed as if the
// bridge never answered.
func (b *codebuddyBackend) denyControlRequest(stdin *codebuddyStdin, requestID, reason string) {
	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response": map[string]any{
				"allowed":  false,
				"behavior": "deny",
				"reason":   reason,
			},
		},
	}
	if err := stdin.writeLine(response); err != nil {
		b.cfg.Logger.Warn("codebuddy: failed to write control response", "error", err)
	}
}
