package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Freebuff account reporting.
//
// A Freebuff runtime is a custom runtime profile whose command is the
// codebuddy-freebuff wrapper. The wrapper owns the account: the token file
// lives on this machine and only this machine can complete a browser login
// for it.
//
// The daemon's job is small. It runs the wrapper, reports what the wrapper
// says, and gets out of the way:
//
//   - the account snapshot (`--status --json`) — the open session slot, the
//     per-model price, the daily and total credit — refreshed on a timer,
//     because the session slot counts down whether or not anyone is looking;
//   - the browser login (`login --json`) — started when the user clicks
//     "Connect to Freebuff" in the web UI. The wrapper prints the one-time
//     URL on its first line and keeps running until the browser confirms, so
//     the daemon forwards both.
//
// The server stores what the daemon reports and the web UI reads it back.

// freebuffCommandName is the profile command that makes a runtime a Freebuff
// runtime. It is the wrapper's own name on PATH.
const freebuffCommandName = "codebuddy-freebuff"

// freebuffStatusInterval is how often the account snapshot is refreshed.
var freebuffStatusInterval = 60 * time.Second

// freebuffConnectPollInterval is how often the daemon asks the server whether
// the user asked for a browser login. The click is a rare event, so the poll
// is cheap; the server also nudges the daemon over the websocket, and this
// poll is the backstop for a lost nudge.
var freebuffConnectPollInterval = 5 * time.Second

// freebuffStatusTimeout bounds one `--status --json` run. The wrapper makes
// a single HTTP call, so a slow answer means the account host is unreachable.
var freebuffStatusTimeout = 15 * time.Second

// freebuffLoginTimeout bounds one browser login. The wrapper enforces its own
// deadline (5 minutes by default); this is the daemon's backstop.
var freebuffLoginTimeout = 6 * time.Minute

// freebuffConnects tracks the browser logins this daemon has in flight, so a
// second poll does not start a second login for the same runtime.
var freebuffConnects sync.Map // runtimeID -> struct{}

// freebuffRuntimes returns the Freebuff runtimes this daemon owns.
func (d *Daemon) freebuffRuntimes() []Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Runtime
	for _, rt := range d.runtimeIndex {
		if d.isFreebuffRuntimeLocked(rt) {
			out = append(out, rt)
		}
	}
	return out
}

// isFreebuffRuntimeLocked reports whether a runtime is a Freebuff runtime.
// The caller must hold d.mu.
func (d *Daemon) isFreebuffRuntimeLocked(rt Runtime) bool {
	if rt.ProfileID == "" {
		return false
	}
	spec, ok := d.profileLaunchSpecs[rt.ProfileID]
	if !ok {
		return false
	}
	return spec.commandName == freebuffCommandName
}

// freebuffCommandFor returns the resolved wrapper path for a Freebuff runtime.
func (d *Daemon) freebuffCommandFor(runtimeID string) (string, bool) {
	spec, ok := d.customProfileLaunchForRuntime(runtimeID)
	if !ok || spec.path == "" {
		return "", false
	}
	if spec.commandName != freebuffCommandName {
		return "", false
	}
	return spec.path, true
}

// freebuffLoop keeps the Freebuff account state fresh and starts a browser
// login when the user asks for one.
func (d *Daemon) freebuffLoop(ctx context.Context) {
	connectTicker := time.NewTicker(freebuffConnectPollInterval)
	defer connectTicker.Stop()
	statusTicker := time.NewTicker(freebuffStatusInterval)
	defer statusTicker.Stop()

	// One snapshot at startup so the UI has something to show before the
	// first tick.
	for _, rt := range d.freebuffRuntimes() {
		go d.reportFreebuffStatus(ctx, rt)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-connectTicker.C:
			d.checkFreebuffConnects(ctx)
		case <-statusTicker.C:
			for _, rt := range d.freebuffRuntimes() {
				go d.reportFreebuffStatus(ctx, rt)
			}
		}
	}
}

// checkFreebuffConnects asks the server, per Freebuff runtime, whether a
// browser login was requested, and starts one when it was.
func (d *Daemon) checkFreebuffConnects(ctx context.Context) {
	for _, rt := range d.freebuffRuntimes() {
		if _, running := freebuffConnects.Load(rt.ID); running {
			continue
		}
		requested, err := d.client.GetFreebuffConnectPending(ctx, rt.ID)
		if err != nil {
			d.logger.Debug("freebuff connect poll failed", "runtime_id", rt.ID, "error", err)
			continue
		}
		if !requested {
			continue
		}
		freebuffConnects.Store(rt.ID, struct{}{})
		go func(rt Runtime) {
			defer freebuffConnects.Delete(rt.ID)
			d.handleFreebuffConnect(ctx, rt)
		}(rt)
	}
}

// freebuffLoginLine is one JSON line the wrapper prints during a login.
type freebuffLoginLine struct {
	Status    string `json:"status"`
	LoginURL  string `json:"loginUrl"`
	Account   string `json:"account"`
	Error     string `json:"error"`
	ExpiresAt int64  `json:"expiresAt"`
}

// handleFreebuffConnect runs the wrapper's browser login and reports the URL
// it produced and, later, the outcome.
//
// The wrapper prints one JSON line when it has a URL and another when the
// login finishes, so the daemon can forward the URL to the UI while the
// browser is still open.
func (d *Daemon) handleFreebuffConnect(ctx context.Context, rt Runtime) {
	command, ok := d.freebuffCommandFor(rt.ID)
	if !ok {
		d.logger.Warn("freebuff connect: runtime command not resolved", "runtime_id", rt.ID)
		d.reportFreebuffConnect(ctx, rt, map[string]any{
			"state": "failed",
			"error": "the Freebuff wrapper is not available on this computer",
		})
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, freebuffLoginTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, "login", "--json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.reportFreebuffConnect(ctx, rt, map[string]any{
			"state": "failed",
			"error": err.Error(),
		})
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		d.reportFreebuffConnect(ctx, rt, map[string]any{
			"state": "failed",
			"error": err.Error(),
		})
		return
	}

	// WaitDelay keeps a grandchild holding the stdout pipe from parking this
	// goroutine past the deadline.
	cmd.WaitDelay = 2 * time.Second

	reported := ""
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var parsed freebuffLoginLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		switch parsed.Status {
		case "awaiting_login":
			if parsed.LoginURL == "" {
				continue
			}
			d.reportFreebuffConnect(ctx, rt, map[string]any{
				"state":     "awaiting_login",
				"login_url": parsed.LoginURL,
			})
			reported = "awaiting_login"
		case "connected":
			d.reportFreebuffConnect(ctx, rt, map[string]any{
				"state":   "connected",
				"account": parsed.Account,
			})
			reported = "connected"
		case "failed":
			d.reportFreebuffConnect(ctx, rt, map[string]any{
				"state": "failed",
				"error": parsed.Error,
			})
			reported = "failed"
		}
	}

	waitErr := cmd.Wait()
	if reported == "connected" {
		return
	}
	if reported == "failed" {
		return
	}
	message := "the browser login did not complete"
	if waitErr != nil {
		message = waitErr.Error()
	}
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		message = tail
	}
	d.reportFreebuffConnect(ctx, rt, map[string]any{
		"state": "failed",
		"error": message,
	})
}

// reportFreebuffStatus refreshes the account snapshot: the open session
// slot, the per-model price and the credit.
func (d *Daemon) reportFreebuffStatus(ctx context.Context, rt Runtime) {
	command, ok := d.freebuffCommandFor(rt.ID)
	if !ok {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, freebuffStatusTimeout)
	defer cancel()

	out, err := exec.CommandContext(runCtx, command, "--status", "--json").Output()
	if err != nil {
		// A refused account is a normal answer, not a daemon failure: the
		// wrapper exits non-zero and prints the reason. Report it as the
		// account's own message so the UI can show it.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			d.reportFreebuffAccount(ctx, rt, map[string]any{
				"status": map[string]any{
					"message": strings.TrimSpace(string(exitErr.Stderr)),
				},
			})
			return
		}
		d.logger.Debug("freebuff status failed", "runtime_id", rt.ID, "error", err)
		return
	}
	var status map[string]any
	if err := json.Unmarshal(out, &status); err != nil {
		d.logger.Debug("freebuff status is not JSON", "runtime_id", rt.ID, "error", err)
		return
	}
	d.logger.Debug("freebuff account refreshed",
		"runtime_id", rt.ID, "summary", freebuffAccountSummary(status))
	d.reportFreebuffAccount(ctx, rt, map[string]any{"status": status})
}

// reportFreebuffAccount delivers an account report to the server.
func (d *Daemon) reportFreebuffAccount(ctx context.Context, rt Runtime, payload map[string]any) {
	d.reportRuntimeResultWithRetry(ctx, "freebuff_account", rt.ID, "", func(ctx context.Context) error {
		return d.client.ReportFreebuffAccount(ctx, rt.ID, payload)
	})
}

// reportFreebuffConnect delivers a browser-login state to the server.
func (d *Daemon) reportFreebuffConnect(ctx context.Context, rt Runtime, connect map[string]any) {
	d.reportFreebuffAccount(ctx, rt, map[string]any{"connect": connect})
}

// freebuffAccountSummary is a short human-readable description of an account
// snapshot, used in daemon logs so a support request carries the state
// without the whole JSON.
func freebuffAccountSummary(status map[string]any) string {
	if status == nil {
		return "no account snapshot"
	}
	fields := []string{}
	if state, ok := status["status"].(string); ok && state != "" {
		fields = append(fields, "session="+state)
	}
	if remaining, ok := status["remainingMs"].(float64); ok {
		fields = append(fields, fmt.Sprintf("remaining=%.0fms", remaining))
	}
	return strings.Join(fields, " ")
}
