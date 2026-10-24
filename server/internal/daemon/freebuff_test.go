package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// freebuffFixture stands up a Daemon whose Freebuff reports are captured.
type freebuffFixture struct {
	daemon *Daemon

	mu      sync.Mutex
	reports []map[string]any
	pending string // body served for the daemon's pending poll
}

func newFreebuffFixture(t *testing.T) *freebuffFixture {
	t.Helper()
	withFastLocalSkillReportBackoffs(t)

	fx := &freebuffFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fx.mu.Lock()
			body := fx.pending
			fx.mu.Unlock()
			_, _ = w.Write([]byte(body))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fx.mu.Lock()
		fx.reports = append(fx.reports, body)
		fx.mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	d := freshDaemon(srv.URL)
	fx.daemon = d
	return fx
}

func (fx *freebuffFixture) snapshot() []map[string]any {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]map[string]any(nil), fx.reports...)
}

// fakeAccount is an orchestrator stand-in for the account flow.
type fakeAccount struct {
	mu       sync.Mutex
	statuses []freebuffrt.AuthStatus // returned in order; the last repeats
	login    freebuffrt.LoginStart
	loginErr error
	canceled bool
	allowance json.RawMessage
}

func (f *fakeAccount) AccountSettings(context.Context, string) (json.RawMessage, error) {
	if f.allowance == nil {
		return nil, errors.New("account host unreachable")
	}
	return f.allowance, nil
}

func (f *fakeAccount) AuthStatus(context.Context) (freebuffrt.AuthStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return st, nil
}

func (f *fakeAccount) StartLogin(context.Context) (freebuffrt.LoginStart, error) {
	return f.login, f.loginErr
}

func (f *fakeAccount) CancelLogin(context.Context) error {
	f.mu.Lock()
	f.canceled = true
	f.mu.Unlock()
	return nil
}

func useFakeAccount(t *testing.T, acct freebuffAccount, startErr error) {
	t.Helper()
	prev := startFreebuffAccount
	startFreebuffAccount = func(context.Context, string, string) (freebuffAccount, func(), error) {
		if startErr != nil {
			return nil, nil, startErr
		}
		return acct, func() {}, nil
	}
	t.Cleanup(func() { startFreebuffAccount = prev })
}

// useProfiles points the Freebuff profile root at a fresh directory.
func useProfiles(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(agent.FreebuffEnvProfilesDir, root)
	return root
}

func freebuffRuntime() Runtime {
	return Runtime{ID: "rt-fb", Name: "Freebuff", Provider: freebuffProvider, Status: "online"}
}

func signedIn(email string) freebuffrt.AuthStatus {
	return freebuffrt.AuthStatus{Authed: true, User: json.RawMessage(`{"email":"` + email + `"}`)}
}

// accountReports flattens the account entries of every captured report.
func (fx *freebuffFixture) accountReports() []map[string]any {
	var out []map[string]any
	for _, r := range fx.snapshot() {
		entries, _ := r["accounts"].([]any)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func fastLogin(t *testing.T) {
	t.Helper()
	prev := freebuffLoginPollInterval
	freebuffLoginPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { freebuffLoginPollInterval = prev })
}

// The sign-in URL goes out as soon as the orchestrator has it, the outcome
// when the account appears. The account host's page is preferred over the
// loopback one: it is the one that opens from another machine.
func TestHandleFreebuffLogin_ReportsRemoteURLThenOutcome(t *testing.T) {
	fx := newFreebuffFixture(t)
	fastLogin(t)
	useFakeAccount(t, &fakeAccount{
		statuses: []freebuffrt.AuthStatus{{}, {}, signedIn("owner@example.com")},
		login: freebuffrt.LoginStart{
			URL:       "http://127.0.0.1:1234/auth/login/launch/x",
			RemoteURL: "https://freebuff.com/login?auth_code=abc",
		},
	}, nil)
	useProfiles(t)

	fx.daemon.handleFreebuffLogin(context.Background(), freebuffRuntime(), "a1")

	reports := fx.accountReports()
	if len(reports) != 2 {
		t.Fatalf("reports = %#v", reports)
	}
	if reports[0]["state"] != "awaiting_login" || reports[0]["login_url"] != "https://freebuff.com/login?auth_code=abc" {
		t.Fatalf("first report = %#v", reports[0])
	}
	if reports[0]["expires_at"] == nil {
		t.Fatalf("the link's expiry was not reported: %#v", reports[0])
	}
	if reports[1]["state"] != "connected" || reports[1]["label"] != "owner@example.com" || reports[1]["id"] != "a1" {
		t.Fatalf("second report = %#v", reports[1])
	}
}

func TestHandleFreebuffLogin_FallsBackToTheLoopbackURL(t *testing.T) {
	fx := newFreebuffFixture(t)
	fastLogin(t)
	useFakeAccount(t, &fakeAccount{
		statuses: []freebuffrt.AuthStatus{{}, signedIn("a@b.c")},
		login:    freebuffrt.LoginStart{URL: "http://127.0.0.1:1234/auth/login/launch/x"},
	}, nil)
	useProfiles(t)

	fx.daemon.handleFreebuffLogin(context.Background(), freebuffRuntime(), "a1")

	if got := fx.accountReports()[0]["login_url"]; got != "http://127.0.0.1:1234/auth/login/launch/x" {
		t.Fatalf("login_url = %v", got)
	}
}

func TestHandleFreebuffLogin_AlreadySignedIn(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("a@b.c")}}, nil)
	useProfiles(t)

	fx.daemon.handleFreebuffLogin(context.Background(), freebuffRuntime(), "a1")

	reports := fx.accountReports()
	if len(reports) != 1 || reports[0]["state"] != "connected" {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestHandleFreebuffLogin_FailuresAreReported(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, nil, errors.New("bun not found"))
	useProfiles(t)

	fx.daemon.handleFreebuffLogin(context.Background(), freebuffRuntime(), "a1")

	r := fx.accountReports()[0]
	if r["state"] != "failed" || r["error"] != "bun not found" {
		t.Fatalf("report = %#v", r)
	}
}

func TestHandleFreebuffLogin_TimeoutCancelsLogin(t *testing.T) {
	fx := newFreebuffFixture(t)
	prevTimeout, prevPoll := freebuffLoginTimeout, freebuffLoginPollInterval
	freebuffLoginTimeout, freebuffLoginPollInterval = 80*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { freebuffLoginTimeout, freebuffLoginPollInterval = prevTimeout, prevPoll })
	acct := &fakeAccount{statuses: []freebuffrt.AuthStatus{{}}, login: freebuffrt.LoginStart{URL: "http://127.0.0.1:1/x"}}
	useFakeAccount(t, acct, nil)
	useProfiles(t)

	fx.daemon.handleFreebuffLogin(context.Background(), freebuffRuntime(), "a1")

	reports := fx.accountReports()
	if last := reports[len(reports)-1]; last["state"] != "failed" {
		t.Fatalf("last report = %#v", last)
	}
	acct.mu.Lock()
	defer acct.mu.Unlock()
	if !acct.canceled {
		t.Fatal("pending login was not cancelled")
	}
}

// A sign-in cancelled because the user removed the account reports nothing:
// nobody is left to read it, and a "failed" would resurrect the entry.
func TestHandleFreebuffLogin_CancelledByRemovalIsSilent(t *testing.T) {
	fx := newFreebuffFixture(t)
	fastLogin(t)
	acct := &fakeAccount{statuses: []freebuffrt.AuthStatus{{}}, login: freebuffrt.LoginStart{URL: "http://127.0.0.1:1/x"}}
	useFakeAccount(t, acct, nil)
	useProfiles(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		fx.daemon.handleFreebuffLogin(ctx, freebuffRuntime(), "a1")
		close(done)
	}()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("login did not stop")
	}
	for _, r := range fx.accountReports() {
		if r["state"] == "failed" {
			t.Fatalf("cancellation reported as failure: %#v", r)
		}
	}
}

// Each account on disk is asked; an account that cannot be asked is reported
// as present with no state, and the report is complete so the server can drop
// accounts that are gone.
func TestReportFreebuffAccounts_CompleteSnapshot(t *testing.T) {
	fx := newFreebuffFixture(t)
	root := useProfiles(t)
	for _, id := range []string{"a1", "a2"} {
		if err := os.MkdirAll(filepath.Join(root, "rt-fb", "accounts", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prev := startFreebuffAccount
	t.Cleanup(func() { startFreebuffAccount = prev })
	startFreebuffAccount = func(_ context.Context, _, accountID string) (freebuffAccount, func(), error) {
		if accountID == "a2" {
			return nil, nil, errors.New("profile busy")
		}
		return &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("owner@example.com")}}, func() {}, nil
	}

	fx.daemon.reportFreebuffAccounts(context.Background(), freebuffRuntime())

	reports := fx.snapshot()
	if len(reports) != 1 || reports[0]["complete"] != true {
		t.Fatalf("reports = %#v", reports)
	}
	entries := fx.accountReports()
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0]["id"] != "a1" || entries[0]["state"] != "connected" || entries[0]["label"] != "owner@example.com" {
		t.Fatalf("a1 = %#v", entries[0])
	}
	if entries[1]["id"] != "a2" || entries[1]["state"] != nil {
		t.Fatalf("a busy account must be reported present, state unchanged: %#v", entries[1])
	}
}

func TestReportFreebuffAccounts_SignedOut(t *testing.T) {
	fx := newFreebuffFixture(t)
	root := useProfiles(t)
	if err := os.MkdirAll(filepath.Join(root, "rt-fb", "accounts", "a1"), 0o700); err != nil {
		t.Fatal(err)
	}
	useFakeAccount(t, &fakeAccount{statuses: []freebuffrt.AuthStatus{{}}}, nil)

	fx.daemon.reportFreebuffAccounts(context.Background(), freebuffRuntime())

	if got := fx.accountReports()[0]["state"]; got != "signed_out" {
		t.Fatalf("state = %v", got)
	}
}

// Removing deletes the login from disk, tells the server what remains, and
// the default follows the server's choice.
func TestSyncFreebuffRuntime_RemovesAccountsAndFollowsDefault(t *testing.T) {
	fx := newFreebuffFixture(t)
	root := useProfiles(t)
	for _, id := range []string{"keep", "bye"} {
		if err := os.MkdirAll(filepath.Join(root, "rt-fb", "accounts", id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fx.mu.Lock()
	fx.pending = `{"accounts":[{"id":"bye","state":"removing"}],"default_account_id":"keep"}`
	fx.mu.Unlock()

	fx.daemon.syncFreebuffRuntime(context.Background(), freebuffRuntime())

	if _, err := os.Stat(filepath.Join(root, "rt-fb", "accounts", "bye")); !os.IsNotExist(err) {
		t.Fatalf("removed account still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "rt-fb", "accounts", "keep")); err != nil {
		t.Fatalf("other account was touched: %v", err)
	}
	id, err := agent.FreebuffAccountForRun(agent.Config{RuntimeID: "rt-fb"})
	if err != nil || id != "keep" {
		t.Fatalf("default account = %q, %v", id, err)
	}
	reports := fx.snapshot()
	if len(reports) != 1 || reports[0]["complete"] != true {
		t.Fatalf("reports = %#v", reports)
	}
	entries := fx.accountReports()
	if len(entries) != 1 || entries[0]["id"] != "keep" {
		t.Fatalf("the report must list what remains: %#v", entries)
	}
}

func TestFreebuffRuntimes_SelectsByProvider(t *testing.T) {
	fx := newFreebuffFixture(t)
	d := fx.daemon
	d.runtimeIndex["rt-fb"] = freebuffRuntime()
	d.runtimeIndex["rt-other"] = Runtime{ID: "rt-other", Provider: "codebuddy"}

	got := d.freebuffRuntimes()
	if len(got) != 1 || got[0].ID != "rt-fb" {
		t.Fatalf("freebuffRuntimes = %#v", got)
	}
}

func TestFreebuffSignedInReportCarriesTheAllowance(t *testing.T) {
	acct := &fakeAccount{allowance: json.RawMessage(`{"status":"none","accessTier":"limited","freebucks":{"balance":25,"daily":{"limit":25,"spent":0,"remaining":25,"resetAt":"2026-10-24T17:00:00.000Z"},"wallet":{"balance":0},"prices":{"mimo/mimo-v2.5":10,"upstage/solar-mini4":0}}}`)}
	st := freebuffrt.AuthStatus{Authed: true, User: json.RawMessage(`{"email":"owner@example.com"}`)}

	status := freebuffSignedInReport(context.Background(), acct, "a1", st)["status"].(map[string]any)
	credit := status["credit"].(map[string]any)
	if credit["dailyRemaining"] != 25.0 || credit["dailyLimit"] != 25.0 || credit["walletBalance"] != 0.0 || credit["dailyResetAt"] != "2026-10-24T17:00:00.000Z" {
		t.Fatalf("credit = %v", credit)
	}
	if prices := status["prices"].(map[string]any); prices["mimo/mimo-v2.5"] != 10.0 || prices["upstage/solar-mini4"] != 0.0 {
		t.Fatalf("prices = %v", prices)
	}
	if status["accessTier"] != "limited" || status["message"] != "Signed in as owner@example.com" {
		t.Fatalf("status = %v", status)
	}
}

func TestFreebuffSignedInReportSurvivesAnUnreachableAccountHost(t *testing.T) {
	st := freebuffrt.AuthStatus{Authed: true, User: json.RawMessage(`{"email":"owner@example.com"}`)}
	r := freebuffSignedInReport(context.Background(), &fakeAccount{}, "a1", st)
	status := r["status"].(map[string]any)
	if r["state"] != "connected" || status["message"] != "Signed in as owner@example.com" || status["credit"] != nil {
		t.Fatalf("report = %v", r)
	}
}
