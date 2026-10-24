package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// Freebuff account reporting.
//
// A Freebuff runtime is a runtime whose provider is "freebuff". Its account is
// the orchestrator profile directory on this machine (one runtime, one
// account): the login lives there, and only this machine can complete a
// browser login for it.
//
// The daemon's job is small. It starts the headless orchestrator, reports what
// it says, and gets out of the way:
//
//   - the account snapshot — whether the profile is signed in, and as whom —
//     refreshed on a timer;
//   - the browser login — started when the user clicks "Connect to Freebuff"
//     in the web UI. The orchestrator hands back a one-time URL and keeps
//     running until the browser confirms, so the daemon forwards the URL and
//     then waits for the account to appear.
//
// The server stores what the daemon reports and the web UI reads it back.
//
// The login URL is a loopback page on the orchestrator, so it opens only in a
// browser on the daemon's own machine.

// freebuffProvider is the provider value that makes a runtime a Freebuff
// runtime.
const freebuffProvider = "freebuff"

// freebuffStatusInterval is how often the account snapshot is refreshed. Each
// refresh starts an orchestrator, so this is slower than a plain HTTP poll.
var freebuffStatusInterval = 5 * time.Minute

// freebuffConnectPollInterval is how often the daemon asks the server whether
// the user asked for a browser login. The click is a rare event, so the poll
// is cheap; the server also nudges the daemon over the websocket, and this
// poll is the backstop for a lost nudge.
var freebuffConnectPollInterval = 5 * time.Second

// freebuffStatusTimeout bounds one status refresh, orchestrator start included.
var freebuffStatusTimeout = 45 * time.Second

// freebuffLoginTimeout bounds one browser login.
var freebuffLoginTimeout = 6 * time.Minute

// freebuffLoginPollInterval is how often a pending login is checked.
var freebuffLoginPollInterval = 2 * time.Second

// freebuffConnects tracks the browser logins this daemon has in flight, so a
// second poll does not start a second login for the same runtime.
var freebuffConnects sync.Map // runtimeID -> struct{}

// freebuffAccount is the slice of the orchestrator the account flow uses. It
// is satisfied by *freebuffrt.Client and replaced in tests.
type freebuffAccount interface {
	AuthStatus(ctx context.Context) (freebuffrt.AuthStatus, error)
	StartLogin(ctx context.Context) (freebuffrt.LoginStart, error)
	CancelLogin(ctx context.Context) error
}

// startFreebuffAccount launches the orchestrator for a runtime's profile and
// returns its client and a stop function. It is a field-like variable so tests
// can run the account flow without a real orchestrator.
var startFreebuffAccount = func(ctx context.Context, runtimeID string) (freebuffAccount, func(), error) {
	cfg := agent.Config{RuntimeID: runtimeID, Logger: nil}
	dir, err := agent.FreebuffProfileDir(cfg)
	if err != nil {
		return nil, nil, err
	}
	orch, err := agent.StartFreebuffOrchestrator(ctx, cfg, dir)
	if err != nil {
		return nil, nil, err
	}
	return orch.Client, orch.Stop, nil
}

// freebuffRuntimes returns the Freebuff runtimes this daemon owns.
func (d *Daemon) freebuffRuntimes() []Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Runtime
	for _, rt := range d.runtimeIndex {
		if rt.Provider == freebuffProvider {
			out = append(out, rt)
		}
	}
	return out
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
				if _, running := freebuffConnects.Load(rt.ID); running {
					continue
				}
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

// freebuffAccountLabel names the signed-in account for display: the email,
// else the name, from the orchestrator's user record.
func freebuffAccountLabel(user json.RawMessage) string {
	var u struct {
		Email string `json:"email"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if json.Unmarshal(user, &u) != nil {
		return ""
	}
	for _, v := range []string{u.Email, u.Name, u.Login} {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// handleFreebuffConnect runs the orchestrator's browser login and reports the
// URL it produced and, later, the outcome. The orchestrator stays up for the
// whole wait: the browser confirms the login against it.
func (d *Daemon) handleFreebuffConnect(ctx context.Context, rt Runtime) {
	fail := func(message string) {
		d.reportFreebuffConnect(ctx, rt, map[string]any{"state": "failed", "error": message})
	}

	runCtx, cancel := context.WithTimeout(ctx, freebuffLoginTimeout)
	defer cancel()

	client, stop, err := startFreebuffAccount(runCtx, rt.ID)
	if err != nil {
		fail(err.Error())
		return
	}
	defer stop()

	// An account that is already signed in needs no login.
	if st, err := client.AuthStatus(runCtx); err == nil && st.Authed {
		d.reportFreebuffConnect(ctx, rt, map[string]any{
			"state":   "connected",
			"account": freebuffAccountLabel(st.User),
		})
		return
	}

	login, err := client.StartLogin(runCtx)
	if err != nil {
		fail(err.Error())
		return
	}
	if login.URL == "" {
		fail("the Freebuff orchestrator did not return a login URL")
		return
	}
	d.reportFreebuffConnect(ctx, rt, map[string]any{
		"state":      "awaiting_login",
		"login_url":  login.URL,
		"expires_at": time.Now().Add(freebuffLoginTimeout).UTC().Format(time.RFC3339),
	})

	ticker := time.NewTicker(freebuffLoginPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			cancelCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			_ = client.CancelLogin(cancelCtx)
			c()
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				fail("the browser login did not complete in time")
			} else {
				fail("the browser login was interrupted")
			}
			return
		case <-ticker.C:
			st, err := client.AuthStatus(runCtx)
			if err != nil {
				continue
			}
			if st.Authed {
				d.reportFreebuffConnect(ctx, rt, map[string]any{
					"state":   "connected",
					"account": freebuffAccountLabel(st.User),
				})
				// Refresh the snapshot now that the account exists.
				d.reportFreebuffAccount(ctx, rt, map[string]any{"status": freebuffStatusPayload(st)})
				return
			}
			if st.Login.Phase == "failed" || st.Login.Phase == "error" || st.Login.Phase == "cancelled" {
				fail("the browser login " + st.Login.Phase)
				return
			}
		}
	}
}

// freebuffStatusPayload is the account snapshot the UI card reads. The
// orchestrator exposes sign-in state only; session slot, credit and prices are
// left out until the card is taught to read them from the orchestrator.
func freebuffStatusPayload(st freebuffrt.AuthStatus) map[string]any {
	if !st.Authed {
		return map[string]any{"message": "Not signed in to Freebuff"}
	}
	label := freebuffAccountLabel(st.User)
	if label == "" {
		return map[string]any{"message": "Signed in to Freebuff"}
	}
	return map[string]any{"message": "Signed in as " + label}
}

// reportFreebuffStatus refreshes the account snapshot.
func (d *Daemon) reportFreebuffStatus(ctx context.Context, rt Runtime) {
	runCtx, cancel := context.WithTimeout(ctx, freebuffStatusTimeout)
	defer cancel()

	client, stop, err := startFreebuffAccount(runCtx, rt.ID)
	if err != nil {
		// A busy profile (a task is using the orchestrator) or a missing
		// bundle is a normal answer, not a daemon failure: keep the last
		// snapshot and say why in the log.
		d.logger.Debug("freebuff status unavailable", "runtime_id", rt.ID, "error", err)
		return
	}
	defer stop()

	st, err := client.AuthStatus(runCtx)
	if err != nil {
		d.logger.Debug("freebuff status failed", "runtime_id", rt.ID, "error", err)
		return
	}
	d.reportFreebuffAccount(ctx, rt, map[string]any{"status": freebuffStatusPayload(st)})
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
