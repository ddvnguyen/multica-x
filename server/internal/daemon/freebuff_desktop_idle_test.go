package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// Idle-close fires after 5 min without a task (M-254).
func TestDesktopIdleCloseFires(t *testing.T) {
	fx := newFreebuffFixture(t)
	rt := Runtime{ID: "idle-close"}
	key := freebuffDesktopLoginKey(rt.ID, "a")
	t.Cleanup(func() { freebuffAccountPins.Delete(key) })
	fx.pending = `{"pins":{"a":"1"},"accounts":[]}`
	stopped := 0
	fx.daemon.freebuffDesktopInstances.Store(key, &freebuffDesktopInstance{
		client:     &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("x")}},
		stop:       func() { stopped++ },
		pin:        "1",
		connected:  true,
		label:      "x",
		lastUsedAt: time.Now().Add(-6 * time.Minute),
	})
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	if stopped != 1 {
		t.Fatalf("stopped=%d, want idle instance closed", stopped)
	}
	if _, ok := fx.daemon.freebuffDesktopInstances.Load(key); ok {
		t.Fatal("idle instance still in map")
	}
	if _, idled := fx.daemon.freebuffDesktopIdleAccounts[key]; !idled {
		t.Fatal("idle close did not cache last-known state")
	}
}

// Reserved, logging-in, and busy accounts are never closed (M-254).
func TestDesktopIdleCloseSkipsReservedLoginBusy(t *testing.T) {
	fx := newFreebuffFixture(t)
	rt := Runtime{ID: "idle-skip"}
	old := time.Now().Add(-10 * time.Minute)
	cases := map[string]string{"r": "reserved", "l": "login", "b": "busy"}
	for id := range cases {
		key := freebuffDesktopLoginKey(rt.ID, id)
		t.Cleanup(func() { freebuffAccountPins.Delete(key) })
		fx.daemon.freebuffDesktopInstances.Store(key, &freebuffDesktopInstance{
			client: &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("x")}},
			stop:   func() {}, pin: "1", lastUsedAt: old,
		})
	}
	release := fx.daemon.reserveFreebuffDesktopAccount(rt.ID, "r")
	defer release()
	lkey := freebuffDesktopLoginKey(rt.ID, "l")
	fx.daemon.freebuffDesktopLogins.Store(lkey, context.CancelFunc(func() {}))
	defer fx.daemon.freebuffDesktopLogins.Delete(lkey)
	fx.pending = `{"pins":{"r":"1","l":"1","b":"1"},"accounts":[{"id":"b","active":true}]}`
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	for id := range cases {
		if _, ok := fx.daemon.freebuffDesktopInstances.Load(freebuffDesktopLoginKey(rt.ID, id)); !ok {
			t.Fatalf("%s instance was closed", cases[id])
		}
		if _, idled := fx.daemon.freebuffDesktopIdleAccounts[freebuffDesktopLoginKey(rt.ID, id)]; idled {
			t.Fatalf("%s instance was idled", cases[id])
		}
	}
}

// Fresh instances (zero clock, e.g. pre-M-254 fixtures) are not reaped.
func TestDesktopIdleCloseSkipsFresh(t *testing.T) {
	fx := newFreebuffFixture(t)
	rt := Runtime{ID: "idle-fresh"}
	key := freebuffDesktopLoginKey(rt.ID, "a")
	t.Cleanup(func() { freebuffAccountPins.Delete(key) })
	fx.pending = `{"pins":{"a":"1"},"accounts":[]}`
	stopped := 0
	fx.daemon.freebuffDesktopInstances.Store(key, &freebuffDesktopInstance{
		client: &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("x")}},
		stop:   func() { stopped++ }, pin: "1",
	})
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	if stopped != 0 {
		t.Fatal("fresh instance with zero clock was reaped")
	}
	// Recent use is not idle either.
	if v, ok := fx.daemon.freebuffDesktopInstances.Load(key); ok {
		v.(*freebuffDesktopInstance).lastUsedAt = time.Now()
	}
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	if stopped != 0 {
		t.Fatal("recently used instance was reaped")
	}
}

// Only task release refreshes the clock; the 5s sync poll must not (M-254 Q1).
func TestDesktopIdleClockRefreshOnlyOnTaskRelease(t *testing.T) {
	fx := newFreebuffFixture(t)
	rt := Runtime{ID: "idle-clock"}
	key := freebuffDesktopLoginKey(rt.ID, "a")
	t.Cleanup(func() { freebuffAccountPins.Delete(key) })
	fx.pending = `{"pins":{"a":"1"},"accounts":[]}`
	inst := &freebuffDesktopInstance{
		client: &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("x")}},
		stop:   func() {}, pin: "1", lastUsedAt: time.Now().Add(-4 * time.Minute),
	}
	fx.daemon.freebuffDesktopInstances.Store(key, inst)
	before := inst.lastUsedAt
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	if v, ok := fx.daemon.freebuffDesktopInstances.Load(key); !ok {
		t.Fatal("instance reaped before timeout")
	} else if !v.(*freebuffDesktopInstance).lastUsedAt.Equal(before) {
		t.Fatal("sync poll refreshed the idle clock")
	}
	release := fx.daemon.reserveFreebuffDesktopAccount(rt.ID, "a")
	release()
	if v, ok := fx.daemon.freebuffDesktopInstances.Load(key); !ok {
		t.Fatal("instance gone after release")
	} else if now := v.(*freebuffDesktopInstance).lastUsedAt; !now.After(before) {
		t.Fatalf("task release did not refresh clock: %v <= %v", now, before)
	}
}

// The status report must not restart idle-closed accounts (M-254).
func TestDesktopIdleReportDoesNotRestart(t *testing.T) {
	fx := newFreebuffFixture(t)
	useProfiles(t)
	rt := Runtime{ID: "idle-report", Provider: "codebuddy", Status: "online"}
	cfg := agent.Config{RuntimeID: rt.ID}
	if _, err := agent.EnsureFreebuffAccountDir(cfg, "a1"); err != nil {
		t.Fatal(err)
	}
	starts := 0
	prev := startFreebuffAccount
	startFreebuffAccount = func(context.Context, string, string) (freebuffAccount, func(), error) {
		starts++
		return &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("owner@example.com")}}, func() {}, nil
	}
	t.Cleanup(func() { startFreebuffAccount = prev })
	key := freebuffDesktopLoginKey(rt.ID, "a1")
	fx.daemon.freebuffDesktopInstances.Store(key, &freebuffDesktopInstance{
		client: &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("owner@example.com")}},
		stop:   func() {}, pin: freebuffAccountEngineVersion(rt.ID, "a1"),
		connected: true, label: "owner@example.com", lastUsedAt: time.Now().Add(-6 * time.Minute),
	})
	fx.pending = `{"pins":{},"accounts":[]}`
	fx.daemon.syncFreebuffDesktopRuntime(context.Background(), rt)
	if _, ok := fx.daemon.freebuffDesktopInstances.Load(key); ok {
		t.Fatal("idle instance not closed by sync")
	}
	fx.daemon.reportFreebuffDesktopAccounts(context.Background(), rt)
	if starts != 0 {
		t.Fatalf("report restarted idle account (%d starts)", starts)
	}
	if _, ok := fx.daemon.freebuffDesktopInstances.Load(key); ok {
		t.Fatal("report recreated the idle instance")
	}
	reports := fx.accountReports()
	if len(reports) == 0 || reports[len(reports)-2]["state"] != "connected" {
		t.Fatalf("idle account lost its connected state: %#v", reports)
	}
	if reports[len(reports)-2]["label"] != "owner@example.com" {
		t.Fatalf("idle account lost its label: %#v", reports[len(reports)-2])
	}
}

// Next task after idle close restarts transparently (M-254).
func TestDesktopIdleRestartOnNextEnsure(t *testing.T) {
	fx := newFreebuffFixture(t)
	rt := Runtime{ID: "idle-restart"}
	key := freebuffDesktopLoginKey(rt.ID, "a")
	t.Cleanup(func() { freebuffAccountPins.Delete(key) })
	live := &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("x")}}
	prev := startFreebuffAccount
	startFreebuffAccount = func(context.Context, string, string) (freebuffAccount, func(), error) {
		return live, func() {}, nil
	}
	t.Cleanup(func() { startFreebuffAccount = prev })
	fx.daemon.freebuffDesktopIdleAccounts = map[string]freebuffDesktopIdleInfo{
		key: {connected: true, label: "x", endpoint: live.BaseURL()},
	}
	client, err := fx.daemon.ensureFreebuffDesktopInstance(context.Background(), rt, "a")
	if err != nil || client != live {
		t.Fatalf("ensure after idle: client=%v err=%v", client, err)
	}
	if _, idled := fx.daemon.freebuffDesktopIdleAccounts[key]; idled {
		t.Fatal("idle cache not cleared on restart")
	}
	if v, ok := fx.daemon.freebuffDesktopInstances.Load(key); !ok || v.(*freebuffDesktopInstance).lastUsedAt.IsZero() {
		t.Fatal("restarted instance missing idle clock")
	}
}
