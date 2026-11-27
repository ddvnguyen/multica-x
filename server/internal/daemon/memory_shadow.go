package daemon

// Memory shadow read (M-616, docs/powercontext-seam.md).
//
// The seam above injects one block from one backend. A shadow host reads a
// SECOND backend on the same claim — same trigger, same scope, same query — and
// records what it would have injected. Nothing it returns reaches the agent:
// the injected bytes are the primary backend's, unchanged, and the run's token
// total does not move.
//
// Three rules make that true rather than merely intended:
//
//  1. The fetch runs in its own goroutine and the claim never waits for it, so
//     a slow or dead shadow server cannot delay a task. A call still in flight
//     when the run ends is recorded as such instead of being waited for.
//  2. It has its own, shorter timeout than the primary seam: a comparison read
//     is worth less than the run it is measuring.
//  3. Every outcome — ready, empty, timeout, unreachable, unreadable scope,
//     switched off in the workspace — is one record on the memory part, so a
//     lane that turned the switch on and sees nothing can tell why.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/initctx"
	"github.com/multica-ai/multica/server/internal/powercontext"
)

const (
	// memoryShadowPowerContext is the only shadow backend today.
	memoryShadowPowerContext = "powercontext"
	// memoryShadowDefaultTimeout bounds the shadow call. Shorter than the
	// primary seam's 10 s on purpose: the shadow is a side read, and a server
	// this slow would not have shaped this run's context anyway.
	memoryShadowDefaultTimeout = 5 * time.Second
)

// memoryShadowTimeout is the live bound. A variable so a test can shorten it
// without a second configuration knob on a lane that should never tune it.
var memoryShadowTimeout = memoryShadowDefaultTimeout

// memoryShadow is one claim's in-flight shadow read. It is a pointer on
// purpose: nil means the switch is off, and the run then has no shadow field at
// all rather than an empty one claiming a read happened.
type memoryShadow struct {
	cfg    Config
	task   Task
	logger *slog.Logger

	mu       sync.Mutex
	report   initctx.MemoryShadowReport
	finished chan struct{}
}

// newMemoryShadow decides whether this claim runs a shadow read. A non-nil
// shadow is started by the caller; a nil shadow with a non-empty note is a
// misconfiguration worth putting on the run row, because the variable is set
// and no comparison will happen — the only place a reader can see that is the
// run they are looking at.
func newMemoryShadow(cfg Config, task Task, logger *slog.Logger) (*memoryShadow, string) {
	setting := strings.ToLower(strings.TrimSpace(cfg.MemoryShadow))
	switch setting {
	case "", "off", "none", "false", "0":
		return nil, ""
	case memoryShadowPowerContext:
	default:
		return nil, fmt.Sprintf("MEMORY_SHADOW=%q is not a memory backend; the shadow read stayed off", strings.TrimSpace(cfg.MemoryShadow))
	}
	if strings.TrimSpace(cfg.PowerContextBaseURL) == "" {
		return nil, "MEMORY_SHADOW=powercontext needs POWERCONTEXT_BASE_URL; the shadow read stayed off"
	}
	if strings.TrimSpace(cfg.MoctBaseURL) == "" {
		return nil, "MEMORY_SHADOW=powercontext records a second block beside the MOCT inject, and this daemon has no MOCT_BASE_URL; the shadow read stayed off"
	}
	return &memoryShadow{cfg: cfg, task: task, logger: logger, finished: make(chan struct{})}, ""
}

// start fires the read. It returns immediately by design: the claim path must
// never wait for the shadow.
func (s *memoryShadow) start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		report := s.fetch(ctx)
		s.mu.Lock()
		s.report = report
		s.mu.Unlock()
		close(s.finished)
	}()
}

// snapshot returns the record for the run row. A read still in flight when the
// run ends is reported as unfinished rather than waited for: the shadow spans
// the claim, not the run, and a run shorter than the call is a fact to record.
func (s *memoryShadow) snapshot() (initctx.MemoryShadowReport, bool) {
	if s == nil {
		return initctx.MemoryShadowReport{}, false
	}
	select {
	case <-s.finished:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.report, true
	default:
		return initctx.MemoryShadowReport{
			Backend: memoryShadowPowerContext,
			Note:    "the shadow call had not answered when the run ended; nothing was compared",
		}, true
	}
}

// wait joins the read with a deadline. Only a test or a diagnostic calls it:
// the run loop records an unfinished read instead of waiting for it.
func (s *memoryShadow) wait(deadline time.Duration) (initctx.MemoryShadowReport, bool) {
	if s == nil {
		return initctx.MemoryShadowReport{}, false
	}
	select {
	case <-s.finished:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.report, true
	case <-time.After(deadline):
		return initctx.MemoryShadowReport{}, false
	}
}

// fetch is the read itself: the same prepare the PowerContext seam would send
// when it owns the host, with a shorter deadline and no injection.
func (s *memoryShadow) fetch(ctx context.Context) (report initctx.MemoryShadowReport) {
	report.Backend = memoryShadowPowerContext
	started := time.Now()
	defer func() { report.LatencyMS = time.Since(started).Milliseconds() }()

	// The same subtraction the primary path makes: a workspace that does not
	// want a pre-run block has nothing to compare against.
	if s.task.MoctMemoryInject != nil && !*s.task.MoctMemoryInject {
		report.Note = "the workspace switched the pre-run memory block off (S2-C, M-123), so there is nothing to compare"
		return report
	}
	scope, ok := powercontext.ScopeForTask(s.task.ProjectID, s.task.WorkspaceID)
	if !ok {
		report.Note = "task carries no project or workspace identity, so there is no memory scope to read"
		return report
	}

	timeout := memoryShadowTimeout
	if timeout <= 0 {
		timeout = memoryShadowDefaultTimeout
	}
	shadowCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := powercontext.NewClient(powercontext.Config{
		BaseURL: s.cfg.PowerContextBaseURL,
		Token:   s.cfg.PowerContextToken,
		Timeout: timeout,
	})
	if err != nil {
		report.Error = "invalid POWERCONTEXT_BASE_URL"
		return report
	}
	prepared, err := client.Prepare(shadowCtx, powercontext.PrepareRequest{
		ScopeID:  scope,
		Query:    powerContextQuery(s.task),
		MaxBytes: powerContextMaxBytes(),
	})
	if err != nil {
		if shadowCtx.Err() == context.DeadlineExceeded {
			report.Error = "timed out after " + timeout.String()
		} else {
			report.Error = powerContextErrorNote(err)
		}
		if s.logger != nil {
			s.logger.Warn("powercontext shadow: prepare failed; nothing compared",
				"task_id", s.task.ID, "scope", scope, "error", err)
		}
		return report
	}
	// Rendered exactly as the primary block would be, so the panel compares two
	// blocks produced by one rule rather than two renderers.
	block := renderPowerContextBlock(prepared.Text())
	report.Block = block
	report.Bytes = len(block)
	report.Tokens = initctx.Estimate(block)
	if block == "" {
		report.Note = "powercontext returned no context for scope " + scope
	}
	return report
}

// applyMemoryShadow lands the shadow record on the report's memory part. Called
// from the deferred init_context assignment, so every terminal path carries it,
// including the fresh-session retry that rebuilds the report.
func applyMemoryShadow(report *initctx.Report, shadow *memoryShadow) {
	if report == nil {
		return
	}
	record, ok := shadow.snapshot()
	if !ok {
		return
	}
	for i := range report.Parts {
		if report.Parts[i].Part == initctx.PartMemory {
			report.Parts[i].Shadow = &record
			return
		}
	}
}
