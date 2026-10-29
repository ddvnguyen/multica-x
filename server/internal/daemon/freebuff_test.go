package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// freebuffFixture stands up a Daemon whose Freebuff reports are captured.
type freebuffFixture struct {
	daemon *Daemon

	mu      sync.Mutex
	reports []map[string]any
}

func newFreebuffFixture(t *testing.T) *freebuffFixture {
	t.Helper()
	withFastLocalSkillReportBackoffs(t)

	fx := &freebuffFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		fx.mu.Lock()
		fx.reports = append(fx.reports, body)
		fx.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
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
	startFreebuffAccount = func(context.Context, string) (freebuffAccount, func(), error) {
		if startErr != nil {
			return nil, nil, startErr
		}
		return acct, func() {}, nil
	}
	t.Cleanup(func() { startFreebuffAccount = prev })
}

func freebuffRuntime() Runtime {
	return Runtime{ID: "rt-fb", Name: "Freebuff", Provider: freebuffProvider, Status: "online"}
}

func signedIn(email string) freebuffrt.AuthStatus {
	return freebuffrt.AuthStatus{Authed: true, User: json.RawMessage(`{"email":"` + email + `"}`)}
}

func TestReportFreebuffStatus_SendsSignInSnapshot(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("owner@example.com")}}, nil)

	fx.daemon.reportFreebuffStatus(context.Background(), freebuffRuntime())

	reports := fx.snapshot()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	status, _ := reports[0]["status"].(map[string]any)
	if status["message"] != "Signed in as owner@example.com" {
		t.Fatalf("status = %#v", reports[0])
	}
}

func TestReportFreebuffStatus_SignedOut(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, &fakeAccount{statuses: []freebuffrt.AuthStatus{{}}}, nil)

	fx.daemon.reportFreebuffStatus(context.Background(), freebuffRuntime())

	status, _ := fx.snapshot()[0]["status"].(map[string]any)
	if status["message"] != "Not signed in to Freebuff" {
		t.Fatalf("status = %#v", status)
	}
}

// A runtime whose orchestrator cannot start reports nothing: the last
// snapshot stays, and the failure is only logged.
func TestReportFreebuffStatus_StartFailureReportsNothing(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, nil, errors.New("bundle not found"))

	fx.daemon.reportFreebuffStatus(context.Background(), freebuffRuntime())

	if got := len(fx.snapshot()); got != 0 {
		t.Fatalf("expected no report, got %d", got)
	}
}

// TestHandleFreebuffConnect_ReportsURLThenOutcome pins the browser login: the
// URL goes out as soon as the orchestrator has it, the outcome when the
// account appears.
func TestHandleFreebuffConnect_ReportsURLThenOutcome(t *testing.T) {
	fx := newFreebuffFixture(t)
	prev := freebuffLoginPollInterval
	freebuffLoginPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { freebuffLoginPollInterval = prev })
	useFakeAccount(t, &fakeAccount{
		statuses: []freebuffrt.AuthStatus{{}, {}, signedIn("owner@example.com")},
		login:    freebuffrt.LoginStart{URL: "http://127.0.0.1:1234/auth/login/launch/x"},
	}, nil)

	fx.daemon.handleFreebuffConnect(context.Background(), freebuffRuntime())

	var states []string
	var url, account string
	for _, r := range fx.snapshot() {
		c, ok := r["connect"].(map[string]any)
		if !ok {
			continue
		}
		states = append(states, c["state"].(string))
		if u, ok := c["login_url"].(string); ok {
			url = u
		}
		if a, ok := c["account"].(string); ok {
			account = a
		}
	}
	if len(states) != 2 || states[0] != "awaiting_login" || states[1] != "connected" {
		t.Fatalf("connect states = %v", states)
	}
	if url != "http://127.0.0.1:1234/auth/login/launch/x" || account != "owner@example.com" {
		t.Fatalf("url=%q account=%q", url, account)
	}
}

func TestHandleFreebuffConnect_AlreadySignedIn(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, &fakeAccount{statuses: []freebuffrt.AuthStatus{signedIn("a@b.c")}}, nil)

	fx.daemon.handleFreebuffConnect(context.Background(), freebuffRuntime())

	reports := fx.snapshot()
	c, _ := reports[0]["connect"].(map[string]any)
	if len(reports) != 1 || c["state"] != "connected" {
		t.Fatalf("reports = %#v", reports)
	}
}

func TestHandleFreebuffConnect_FailuresAreReported(t *testing.T) {
	fx := newFreebuffFixture(t)
	useFakeAccount(t, nil, errors.New("bun not found"))

	fx.daemon.handleFreebuffConnect(context.Background(), freebuffRuntime())

	c, _ := fx.snapshot()[0]["connect"].(map[string]any)
	if c["state"] != "failed" || c["error"] != "bun not found" {
		t.Fatalf("connect = %#v", c)
	}
}

func TestHandleFreebuffConnect_TimeoutCancelsLogin(t *testing.T) {
	fx := newFreebuffFixture(t)
	prevTimeout, prevPoll := freebuffLoginTimeout, freebuffLoginPollInterval
	freebuffLoginTimeout, freebuffLoginPollInterval = 80*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { freebuffLoginTimeout, freebuffLoginPollInterval = prevTimeout, prevPoll })
	acct := &fakeAccount{statuses: []freebuffrt.AuthStatus{{}}, login: freebuffrt.LoginStart{URL: "http://127.0.0.1:1/x"}}
	useFakeAccount(t, acct, nil)

	fx.daemon.handleFreebuffConnect(context.Background(), freebuffRuntime())

	reports := fx.snapshot()
	last, _ := reports[len(reports)-1]["connect"].(map[string]any)
	if last["state"] != "failed" {
		t.Fatalf("last connect = %#v", last)
	}
	acct.mu.Lock()
	defer acct.mu.Unlock()
	if !acct.canceled {
		t.Fatal("pending login was not cancelled")
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
