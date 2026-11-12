package agent

import "context"

// AskOption is one choice an ask offers. The label is what an answer carries;
// the description is display-only and may be empty.
type AskOption struct {
	Label       string
	Description string
}

// AskQuestion is one question inside a single ask. A provider tool call can
// carry several questions at once (CodeBuddy's AskUserQuestion accepts up to
// four), and they are answered as a unit: the caller either returns every
// answer or declines the whole call.
type AskQuestion struct {
	// Header is the short label the provider renders above the prompt. It may
	// be empty; Prompt is always populated.
	Header string
	// Prompt is the question text. It is also the key the provider expects in
	// the answer map, so a caller must not rewrite it.
	Prompt string
	// Options are the offered choices. Empty means the provider is asking for
	// free text.
	Options     []AskOption
	MultiSelect bool
}

// AskRequest is one held tool call.
type AskRequest struct {
	// ToolCallID is the provider's identity for the call, used only for
	// correlation and logging.
	ToolCallID string
	Questions  []AskQuestion
}

// AskAnswer is the outcome of one ask.
//
// The zero value is a decline: a caller that cannot reach a human must say so
// rather than return an empty map, because an empty map is indistinguishable
// from "answered nothing" and the backend would have to invent behaviour.
type AskAnswer struct {
	// Answers maps a question's Prompt to the chosen label (or free text). A
	// multi-select answer joins its labels with ", ".
	Answers map[string]string
	// Declined means nobody answered — the question expired, the run was
	// cancelled, or no channel could reach an addressee. The backend tells the
	// model to proceed on its own assumption instead of blocking forever.
	Declined bool
	// Reason explains a decline. It is shown to the model, so it must read as
	// an instruction, not a status code.
	Reason string
}

// Asker is the daemon's answer channel for a backend that can ask the user
// something mid-run. Ask blocks until the question is answered, expires, or
// ctx is done; it must return promptly once ctx is cancelled.
//
// A backend that receives no Asker must not offer the user-facing ask tool at
// all — an ask it cannot deliver would park the run until the provider's own
// timeout.
type Asker interface {
	Ask(ctx context.Context, req AskRequest) (AskAnswer, error)
}

// SupportsAsk reports whether the backend for provider can park a tool call on
// a question. It is what tells the daemon to offer the question capability
// before building exec options: a provider absent here has no ask tool to
// enable, and offering the capability would negotiate a channel nothing uses.
func SupportsAsk(provider string) bool {
	switch provider {
	case "codebuddy":
		// freebuff resolves to this backend too, via its protocol family.
		return true
	default:
		return false
	}
}
