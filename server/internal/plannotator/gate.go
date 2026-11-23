// Package plannotator wraps the `plannotator` CLI as a human-in-the-loop gate in
// front of a plan or a diff (docs/plannotator-gate.md).
//
// The package exists because Plannotator's only durable interface is its process
// boundary. A review session is a temporary, unauthenticated HTTP server on a
// random port whose UI routes are explicitly version-unstable, and there is no
// webhook for session lifecycle — so the verdict is read from the process: the
// exit code, the JSON record, or the --result-file, never from a URL.
//
// That decision is what keeps the platform the system of record. The plan and
// the verdict live in Multica's own tables; Plannotator renders the plan,
// collects annotations, and stops existing when the session closes.
package plannotator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DefaultBinary is the command looked up on PATH when Config.Binary is empty.
const DefaultBinary = "plannotator"

// DefaultTimeout bounds one gate. A human is in the loop, so the bound is
// hours rather than seconds: Plannotator's own host hooks ship a 345600-second
// (four day) timeout, and a gate that gives up sooner would deny a legitimate
// reviewer the review.
const DefaultTimeout = 24 * time.Hour

// Mode selects the Plannotator surface.
type Mode string

const (
	// ModePlan annotates a plan document (`plannotator annotate`).
	ModePlan Mode = "plan"
	// ModeDiff reviews a caller-supplied unified diff (`plannotator review
	// --patch-file`). The patch form is the portable one: it needs no git
	// repository, so a sandbox or a resolved worktree both work.
	ModeDiff Mode = "diff"
)

// Decision is a review outcome. It mirrors the JSON record Plannotator writes,
// not the prose in its UI: a caller must branch on this value, never on a
// message's wording.
type Decision string

const (
	DecisionApproved  Decision = "approved"
	DecisionAnnotated Decision = "annotated"
	DecisionDismissed Decision = "dismissed"
	// DecisionUnknown covers a payload that did not name a decision.
	DecisionUnknown Decision = ""
)

// Config is static gate configuration for one host.
type Config struct {
	// Binary overrides the executable (default DefaultBinary).
	Binary string
	// Origin forces the calling agent for feedback framing
	// (PLANNOTATOR_ORIGIN): claude-code, codex, opencode, ...
	Origin string
	// Remote forces remote mode (PLANNOTATOR_REMOTE=1): a fixed port and a
	// 0.0.0.0 bind, for a daemon host whose browser is elsewhere.
	Remote bool
	// Tailscale serves the session over `tailscale serve` instead of exposing
	// a port. Loopback stays the bind; it is the safer of the two remote
	// options, and it is refused only when the binary does not support it.
	Tailscale bool
	// Browser overrides the browser launch (PLANNOTATOR_BROWSER). "none"
	// suppresses it on a headless host; the URL is printed instead.
	Browser string
	// Port overrides PLANNOTATOR_PORT (a port, a range, or a single fixed
	// port for remote mode).
	Port string
	// Timeout bounds one gate. Zero means DefaultTimeout.
	Timeout time.Duration

	// logWriter receives the gate's stderr in addition to os.Stderr. Unset
	// means os.Stderr only; tests set it to swallow the forwarded output.
	logWriter io.Writer
}

// Request is one gate invocation.
type Request struct {
	Mode Mode
	// Target is the plan file (ModePlan) or the patch file (ModeDiff). A path
	// of "-" is only meaningful for a diff, where it means stdin.
	Target string
	// ResultFile is where Plannotator writes the JSON verdict. It is used by
	// plan gates (the only mode that accepts --require-approval and
	// --result-file); a diff gate leaves it empty.
	ResultFile string
}

// Verdict is the outcome of one gate.
type Verdict struct {
	Decision Decision
	// Feedback is the reviewer's annotations as Markdown: selected text,
	// comments, deletions, and any directly proposed edits.
	Feedback string
	// ExitCode is the process exit status, kept so a caller can distinguish
	// "the reviewer dismissed the plan" from "the gate itself failed".
	ExitCode int
	// SessionURL is the review link the binary printed on startup, when it
	// printed one. A caller logs it so an operator on a headless daemon host
	// can open the review the reviewer's browser already has; empty is normal.
	SessionURL string
}

// Approved reports whether the reviewer accepted the plan or diff.
func (v *Verdict) Approved() bool { return v != nil && v.Decision == DecisionApproved }

// ErrGateFailed reports that Plannotator exited with the documented gate
// failure status (2): the review did not complete, so there is no verdict to
// apply. Callers must treat this as "no decision", never as a denial.
var ErrGateFailed = errors.New("plannotator: gate failed")

// Run executes one gate and returns its verdict.
//
// Exit codes, per Plannotator's CLI contract: 0 approved, 1 annotated or
// dismissed (the JSON record says which), 2 gate failure, 128+n killed by
// signal n. The record wins over the code when both are present: a reviewer who
// approves with notes returns 0 and a decision of "approved" plus feedback.
func Run(ctx context.Context, cfg Config, req Request) (*Verdict, error) {
	args, err := argsFor(cfg, req)
	if err != nil {
		return nil, err
	}
	binary := strings.TrimSpace(cfg.Binary)
	if binary == "" {
		binary = DefaultBinary
	}
	// Resolve before spawning: a missing binary is a configuration fact the
	// caller reports once, not a failed review the reviewer has to explain.
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("plannotator: %q is not installed or not on PATH: %w", binary, err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Env = append(os.Environ(), cfg.env()...)
	// stdout belongs to the gate's JSON record; stderr carries the session URL
	// and human-readable progress. It is forwarded so a headless reviewer still
	// finds the link in the daemon log, and captured so the caller can surface
	// that same link where a person is already looking.
	logSink := cfg.logWriter
	if logSink == nil {
		logSink = os.Stderr
	}
	stderr := &cappedBuffer{max: 8 << 10}
	cmd.Stderr = io.MultiWriter(logSink, stderr)
	stdout, runErr := cmd.Output()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return nil, fmt.Errorf("plannotator: run %s: %w", binary, runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	verdict := &Verdict{ExitCode: exitCode, Decision: DecisionUnknown, SessionURL: sessionURL(stderr)}
	// The result file is authoritative when the caller asked for one; stdout is
	// the fallback because a gate can be killed after writing neither.
	record := readRecord(req.ResultFile)
	if record == nil {
		record = parseRecord(stdout)
	}
	if record != nil {
		if d := Decision(strings.TrimSpace(record.Decision)); d != DecisionUnknown {
			verdict.Decision = d
		}
		verdict.Feedback = strings.TrimSpace(record.Feedback)
	}
	// Fill a missing decision from the exit code the contract documents. This
	// is the one place the code is trusted over the record, and only because
	// the record was silent.
	if verdict.Decision == DecisionUnknown {
		switch exitCode {
		case 0:
			verdict.Decision = DecisionApproved
		case 1:
			verdict.Decision = DecisionAnnotated
		}
	}
	if exitCode == 2 {
		return verdict, fmt.Errorf("%w (exit 2)", ErrGateFailed)
	}
	if exitCode != 0 && exitCode != 1 && exitCode != 2 {
		return verdict, fmt.Errorf("plannotator: exited with status %d", exitCode)
	}
	if verdict.Decision == DecisionUnknown {
		// Exit 0/1 with no record at all: the gate contradicted its contract.
		return verdict, fmt.Errorf("plannotator: no decision in the result (exit %d)", exitCode)
	}
	return verdict, nil
}

// argsFor builds the argv for a request, rejecting shapes Plannotator does not
// accept: --hook, --require-approval and --result-file are annotate-only, and
// a diff gate has no hook form at all.
func argsFor(cfg Config, req Request) ([]string, error) {
	target := strings.TrimSpace(req.Target)
	if target == "" {
		return nil, errors.New("plannotator: gate needs a target (plan file or patch)")
	}
	var args []string
	switch req.Mode {
	case ModePlan:
		args = []string{"annotate", target, "--gate", "--json", "--require-approval"}
		if resultFile := strings.TrimSpace(req.ResultFile); resultFile != "" {
			args = append(args, "--result-file", resultFile)
		}
	case ModeDiff:
		if strings.TrimSpace(req.ResultFile) != "" {
			return nil, errors.New("plannotator: --result-file is annotate-only; a diff gate reads its exit code")
		}
		args = []string{"review", "--patch-file", target, "--gate", "--json"}
	default:
		return nil, fmt.Errorf("plannotator: unknown gate mode %q", req.Mode)
	}
	if cfg.Tailscale {
		args = append(args, "--tailscale")
	}
	return args, nil
}

// env renders the configuration as PLANNOTATOR_* variables. An empty field is
// omitted so the binary's own defaults (auto-detected remote mode, browser
// launch) stay in force.
func (c Config) env() []string {
	env := make([]string, 0, 5)
	if c.Remote {
		env = append(env, "PLANNOTATOR_REMOTE=1")
	}
	if origin := strings.TrimSpace(c.Origin); origin != "" {
		env = append(env, "PLANNOTATOR_ORIGIN="+origin)
	}
	if browser := strings.TrimSpace(c.Browser); browser != "" {
		env = append(env, "PLANNOTATOR_BROWSER="+browser)
	}
	if port := strings.TrimSpace(c.Port); port != "" {
		env = append(env, "PLANNOTATOR_PORT="+port)
	}
	return env
}

// cappedBuffer keeps the first max bytes written to it and discards the rest.
// A gate's stderr is startup chatter; only the opening line carrying the
// session URL is wanted, and an unbounded copy would let a chatty binary grow
// the daemon's heap for the length of a session that can run for days.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

var sessionURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// sessionURL extracts the first URL a gate printed. Trailing sentence
// punctuation is trimmed so "open http://host:port/." yields the link, not the
// period; a gate that printed none yields "" rather than an error.
func sessionURL(stderr *cappedBuffer) string {
	if stderr == nil {
		return ""
	}
	match := sessionURLPattern.FindString(stderr.buf.String())
	return strings.TrimRight(match, ".,;:)")
}

// record is the subset of Plannotator's JSON record the gate reads. Both modes
// use `decision`; the feedback field is `feedback` for annotate and `message`
// for review, so both are decoded and the non-empty one wins.
type record struct {
	Decision string `json:"decision"`
	Feedback string `json:"feedback"`
	Message  string `json:"message"`
}

// effectiveFeedback is feedback with review mode's `message` fallback applied.
func (r *record) effectiveFeedback() string {
	if r == nil {
		return ""
	}
	if strings.TrimSpace(r.Feedback) != "" {
		return r.Feedback
	}
	return r.Message
}

// parseRecord decodes a JSON record from a byte slice, returning nil when the
// bytes carry no decision. Trailing output is tolerated: a gate may print a URL
// line before its JSON.
func parseRecord(data []byte) *record {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	candidates := []string{trimmed}
	if idx := strings.LastIndex(trimmed, "{"); idx > 0 {
		candidates = append(candidates, trimmed[idx:])
	}
	for _, candidate := range candidates {
		var r record
		if err := json.Unmarshal([]byte(candidate), &r); err != nil {
			continue
		}
		if strings.TrimSpace(r.Decision) != "" {
			r.Feedback = r.effectiveFeedback()
			return &r
		}
	}
	return nil
}

// readRecord reads a result file, returning nil when it is absent, empty, or
// unparseable — all of which fall back to stdout rather than failing the gate.
func readRecord(path string) *record {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseRecord(data)
}
