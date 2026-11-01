package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const freebuffDesktopConnectPollInterval = 5 * time.Second
const freebuffDesktopStatusInterval = 5 * time.Minute

var freebuffDesktopLogins sync.Map // "runtimeID/accountID" -> context.CancelFunc
var freebuffDesktopInstances sync.Map // "runtimeID/accountID" -> *freebuffDesktopInstance

type freebuffDesktopInstance struct {
	client freebuffAccount
	stop   func()
}

func freebuffDesktopLoginKey(runtimeID, accountID string) string { return runtimeID + "/" + accountID }

func (d *Daemon) ensureFreebuffDesktopInstance(ctx context.Context, rt Runtime, accountID string) (freebuffAccount, error) {
	key := freebuffDesktopLoginKey(rt.ID, accountID)
	if v, ok := freebuffDesktopInstances.Load(key); ok {
		inst := v.(*freebuffDesktopInstance)
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := inst.client.AuthStatus(hctx)
		cancel()
		if err == nil {
			return inst.client, nil
		}
		inst.stop()
		freebuffDesktopInstances.Delete(key)
	}
	client, stop, err := startFreebuffAccount(ctx, rt.ID, accountID)
	if err != nil {
		return nil, err
	}
	freebuffDesktopInstances.Store(key, &freebuffDesktopInstance{client: client, stop: stop})
	return client, nil
}

func (d *Daemon) freebuffDesktopRuntimes() []Runtime {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Runtime
	for _, rt := range d.runtimeIndex {
		if rt.Provider != "codebuddy" {
			continue
		}
		if rt.ProfileID == "" {
			continue
		}
		spec, ok := d.profileLaunchSpecs[rt.ProfileID]
		if !ok {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(spec.commandName), "freebuff-desktop") {
			continue
		}
		out = append(out, rt)
	}
	return out
}

func (d *Daemon) freebuffDesktopLoop(ctx context.Context) {
	pollTicker := time.NewTicker(freebuffDesktopConnectPollInterval)
	defer pollTicker.Stop()
	statusTicker := time.NewTicker(freebuffDesktopStatusInterval)
	defer statusTicker.Stop()

	for _, rt := range d.freebuffDesktopRuntimes() {
		go d.syncFreebuffDesktopRuntime(ctx, rt)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			for _, rt := range d.freebuffDesktopRuntimes() {
				d.syncFreebuffDesktopRuntime(ctx, rt)
			}
		case <-statusTicker.C:
			for _, rt := range d.freebuffDesktopRuntimes() {
				go d.reportFreebuffDesktopAccounts(ctx, rt)
			}
		}
	}
}

func (d *Daemon) syncFreebuffDesktopRuntime(ctx context.Context, rt Runtime) {
	pending, err := d.client.GetFreebuffDesktopPending(ctx, rt.ID)
	if err != nil {
		d.logger.Debug("freebuff-desktop pending poll failed", "runtime_id", rt.ID, "error", err)
		return
	}
	for _, acc := range pending.Accounts {
		if !agent.ValidFreebuffAccountID(acc.ID) {
			continue
		}
		key := freebuffDesktopLoginKey(rt.ID, acc.ID)
		switch acc.State {
		case "requested", "awaiting_login":
			if _, running := freebuffDesktopLogins.Load(key); running {
				continue
			}
			loginCtx, cancel := context.WithCancel(ctx)
			freebuffDesktopLogins.Store(key, context.CancelFunc(cancel))
			go func(accountID string) {
				defer freebuffDesktopLogins.Delete(key)
				defer cancel()
				d.handleFreebuffDesktopLogin(loginCtx, rt, accountID)
			}(acc.ID)
		case "removing":
			cancel, ok := freebuffDesktopLogins.Load(key)
			if ok {
				cancel.(context.CancelFunc)()
				for i := 0; i < 30; i++ {
					if _, running := freebuffDesktopLogins.Load(key); !running {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			key := freebuffDesktopLoginKey(rt.ID, acc.ID)
			if v, ok := freebuffDesktopInstances.Load(key); ok {
				v.(*freebuffDesktopInstance).stop()
				freebuffDesktopInstances.Delete(key)
			}
			cfg := agent.Config{RuntimeID: rt.ID}
			_ = agent.RemoveFreebuffAccount(cfg, acc.ID)
			d.reportFreebuffDesktopEntries(ctx, rt, []map[string]any{{"id": acc.ID}}, true)
		}
	}
}

func freebuffDesktopAccountDir(rt Runtime, accountID string) string {
	dir, err := agent.FreebuffAccountDir(agent.Config{RuntimeID: rt.ID}, accountID)
	if err != nil {
		return accountID
	}
	return dir
}

func (d *Daemon) handleFreebuffDesktopLogin(ctx context.Context, rt Runtime, accountID string) {
	report := func(entry map[string]any) {
		d.reportFreebuffDesktopEntries(ctx, rt, []map[string]any{entry}, false)
	}
	fail := func(message string) {
		report(map[string]any{"id": accountID, "state": "failed", "error": message})
	}

	runCtx, cancel := context.WithTimeout(ctx, freebuffLoginTimeout)
	defer cancel()

	client, err := d.ensureFreebuffDesktopInstance(ctx, rt, accountID)
	if err != nil {
		fail(err.Error())
		return
	}

	if st, err := client.AuthStatus(runCtx); err == nil && st.Authed {
		report(map[string]any{
			"id":             accountID,
			"state":          "connected",
			"label":          freebuffAccountLabel(st.User),
			"endpoint":       client.BaseURL(),
			"credential_ref": freebuffDesktopAccountDir(rt, accountID),
			"status":         map[string]any{"message": "Signed in to Freebuff"},
		})
		return
	}

	login, err := client.StartLogin(runCtx)
	if err != nil {
		fail(err.Error())
		return
	}
	url := login.RemoteURL
	if url == "" {
		url = login.URL
	}
	if url == "" {
		fail("the Freebuff orchestrator did not return a login URL")
		return
	}
	report(map[string]any{
		"id":             accountID,
		"state":          "awaiting_login",
		"login_url":      url,
		"expires_at":     time.Now().Add(freebuffLoginTimeout).UTC().Format(time.RFC3339),
		"endpoint":       client.BaseURL(),
		"credential_ref": freebuffDesktopAccountDir(rt, accountID),
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
				fail("the sign-in was not completed in time; try again for a new link")
			}
			return
		case <-ticker.C:
			st, err := client.AuthStatus(runCtx)
			if err != nil {
				continue
			}
			if st.Authed {
				label := freebuffAccountLabel(st.User)
				msg := "Signed in to Freebuff"
				if label != "" {
					msg = "Signed in as " + label
				}
				report(map[string]any{
					"id":             accountID,
					"state":          "connected",
					"label":          label,
					"endpoint":       client.BaseURL(),
					"credential_ref": freebuffDesktopAccountDir(rt, accountID),
					"status":         map[string]any{"message": msg},
				})
				return
			}
			if st.Login.Phase == "failed" || st.Login.Phase == "error" || st.Login.Phase == "cancelled" {
				fail("the sign-in " + st.Login.Phase)
				return
			}
		}
	}
}

func (d *Daemon) reportFreebuffDesktopEntries(ctx context.Context, rt Runtime, entries []map[string]any, complete bool) {
	payload := map[string]any{"accounts": entries, "complete": complete}
	d.reportRuntimeResultWithRetry(ctx, "freebuff_desktop_account", rt.ID, "", func(ctx context.Context) error {
		return d.client.ReportFreebuffDesktopAccount(ctx, rt.ID, payload)
	})
}

func (d *Daemon) reportFreebuffDesktopAccounts(ctx context.Context, rt Runtime) {
	cfg := agent.Config{RuntimeID: rt.ID}
	ids, err := agent.ListFreebuffAccounts(cfg)
	if err != nil {
		d.logger.Debug("freebuff-desktop accounts unavailable", "runtime_id", rt.ID, "error", err)
		return
	}
	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, map[string]any{"id": id})
	}
	d.reportFreebuffDesktopEntries(ctx, rt, entries, true)
}
