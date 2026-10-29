package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestParseFreebuffExpiresAt(t *testing.T) {
	want := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for name, raw := range map[string]string{
		"rfc3339": `"2026-10-24T12:00:00Z"`,
		"millis":  `1792843200000`,
	} {
		got, ok := parseFreebuffExpiresAt(json.RawMessage(raw))
		if !ok || !got.Equal(want) {
			t.Fatalf("%s: got %v ok=%v, want %v", name, got, ok, want)
		}
	}
	for _, raw := range []string{``, `null`, `"soon"`, `0`, `-5`} {
		if _, ok := parseFreebuffExpiresAt(json.RawMessage(raw)); ok {
			t.Fatalf("%q should not parse", raw)
		}
	}
}

func accountByID(t *testing.T, b *FreebuffBlock, id string) FreebuffAccount {
	t.Helper()
	i := b.find(id)
	if i < 0 {
		t.Fatalf("account %q missing from %#v", id, b.Accounts)
	}
	return b.Accounts[i]
}

func TestApplyFreebuffReport_LoginThenConnected(t *testing.T) {
	now := time.Now()
	b := &FreebuffBlock{Accounts: []FreebuffAccount{{ID: "a1", State: FreebuffAccountRequested}}}

	applyFreebuffReport(b, freebuffReport{Accounts: []freebuffReportedAccount{{
		ID: "a1", State: FreebuffAccountAwaitingLogin,
		LoginURL: "https://freebuff.com/login?auth_code=x", ExpiresAt: json.RawMessage(`"2026-10-24T12:00:00Z"`),
	}}}, now)
	acc := accountByID(t, b, "a1")
	if acc.State != FreebuffAccountAwaitingLogin || acc.LoginURL == "" || acc.ExpiresAt == nil {
		t.Fatalf("awaiting_login not recorded: %#v", acc)
	}
	if b.DefaultAccountID != "" {
		t.Fatalf("default set before any account is connected: %q", b.DefaultAccountID)
	}

	applyFreebuffReport(b, freebuffReport{Accounts: []freebuffReportedAccount{{
		ID: "a1", State: FreebuffAccountConnected, Label: "me@example.com",
		Status: json.RawMessage(`{"credit":{"dailyRemaining":20}}`),
	}}}, now)
	acc = accountByID(t, b, "a1")
	if acc.State != FreebuffAccountConnected || acc.Label != "me@example.com" {
		t.Fatalf("connected not recorded: %#v", acc)
	}
	if acc.LoginURL != "" || acc.ExpiresAt != nil {
		t.Fatalf("the spent login URL must be cleared: %#v", acc)
	}
	if len(acc.Status) == 0 {
		t.Fatal("status was dropped")
	}
	if b.DefaultAccountID != "a1" {
		t.Fatalf("the first connected account should become the default, got %q", b.DefaultAccountID)
	}
}

func TestApplyFreebuffReport_KeepsRequestsTheDaemonHasNotSeen(t *testing.T) {
	now := time.Now()
	b := &FreebuffBlock{
		Accounts: []FreebuffAccount{
			{ID: "old", State: FreebuffAccountConnected},
			{ID: "new", State: FreebuffAccountRequested},
		},
		DefaultAccountID: "old",
	}
	// A complete report that predates the click on "new".
	applyFreebuffReport(b, freebuffReport{Complete: true, Accounts: []freebuffReportedAccount{
		{ID: "old", State: FreebuffAccountConnected},
	}}, now)
	if accountByID(t, b, "new").State != FreebuffAccountRequested {
		t.Fatal("a request the daemon has not seen was erased")
	}
}

func TestApplyFreebuffReport_CompleteReportDropsVanishedAndRemovedAccounts(t *testing.T) {
	now := time.Now()
	b := &FreebuffBlock{
		Accounts: []FreebuffAccount{
			{ID: "keep", State: FreebuffAccountConnected},
			{ID: "gone", State: FreebuffAccountConnected},
			{ID: "bye", State: FreebuffAccountRemoving},
		},
		DefaultAccountID: "gone",
	}
	applyFreebuffReport(b, freebuffReport{Complete: true, Accounts: []freebuffReportedAccount{
		{ID: "keep", State: FreebuffAccountConnected},
	}}, now)
	if len(b.Accounts) != 1 || b.Accounts[0].ID != "keep" {
		t.Fatalf("accounts = %#v, want only keep", b.Accounts)
	}
	if b.DefaultAccountID != "keep" {
		t.Fatalf("default = %q, want keep", b.DefaultAccountID)
	}
}

func TestApplyFreebuffReport_IgnoresReportsForAnAccountBeingRemoved(t *testing.T) {
	b := &FreebuffBlock{Accounts: []FreebuffAccount{{ID: "a1", State: FreebuffAccountRemoving}}}
	applyFreebuffReport(b, freebuffReport{Accounts: []freebuffReportedAccount{
		{ID: "a1", State: FreebuffAccountConnected},
	}}, time.Now())
	if accountByID(t, b, "a1").State != FreebuffAccountRemoving {
		t.Fatal("a stale report resurrected a removed account")
	}
}

func TestApplyFreebuffReport_RejectsStatesTheDaemonMayNotSet(t *testing.T) {
	b := &FreebuffBlock{}
	applyFreebuffReport(b, freebuffReport{Accounts: []freebuffReportedAccount{
		{ID: "a1", State: FreebuffAccountRequested},
		{ID: "a2", State: "bogus"},
		{ID: "", State: FreebuffAccountConnected},
	}}, time.Now())
	if len(b.Accounts) != 0 {
		t.Fatalf("accounts = %#v, want none", b.Accounts)
	}
}

func TestFreebuffBlockFromMetadata(t *testing.T) {
	if got := freebuffBlockFromMetadata(nil); got.Accounts == nil || len(got.Accounts) != 0 {
		t.Fatalf("empty metadata should give an empty, non-nil list: %#v", got)
	}
	// The pre-multi-account shape is ignored rather than misread.
	old := []byte(`{"freebuff":{"connect":{"state":"connected"},"status":{"message":"x"}}}`)
	if got := freebuffBlockFromMetadata(old); len(got.Accounts) != 0 {
		t.Fatalf("old block produced accounts: %#v", got)
	}
	cur := []byte(`{"freebuff":{"accounts":[{"id":"a1","state":"connected"}],"default_account_id":"a1"}}`)
	if got := freebuffBlockFromMetadata(cur); len(got.Accounts) != 1 || got.DefaultAccountID != "a1" {
		t.Fatalf("current block misread: %#v", got)
	}
}

func freebuffCall(t *testing.T, fn http.HandlerFunc, method, runtimeID, accountID string) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, "/api/runtimes/"+runtimeID+"/freebuff", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("runtimeId", runtimeID)
	if accountID != "" {
		rctx.URLParams.Add("accountId", accountID)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

// The user-facing life of an account against a real row: add, the daemon
// reports a sign-in, default, retry, remove — and a report that lands between
// two clicks does not erase the second.
func TestFreebuffAccountLifecycle(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'freebuff-test-machine', 'Freebuff test runtime', 'local', 'freebuff', 'online', 'x', '{}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	read := func() *FreebuffBlock {
		var metadata []byte
		if err := testPool.QueryRow(ctx, `SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		return freebuffBlockFromMetadata(metadata)
	}
	daemonReports := func(report freebuffReport) {
		t.Helper()
		req := newRequest("POST", "/", nil)
		rt, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testHandler.updateFreebuffBlock(req, rt, func(b *FreebuffBlock) (bool, error) {
			applyFreebuffReport(b, report, time.Now())
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Add two accounts.
	var first, second FreebuffAccount
	for _, into := range []*FreebuffAccount{&first, &second} {
		w := freebuffCall(t, testHandler.AddFreebuffAccount, "POST", runtimeID, "")
		if w.Code != http.StatusAccepted {
			t.Fatalf("add: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), into); err != nil || into.State != FreebuffAccountRequested {
			t.Fatalf("add body: %s", w.Body.String())
		}
	}
	if got := read(); len(got.Accounts) != 2 {
		t.Fatalf("accounts = %#v", got.Accounts)
	}

	// The daemon signs the first one in; the second click's account survives
	// the report.
	daemonReports(freebuffReport{Complete: true, Accounts: []freebuffReportedAccount{
		{ID: first.ID, State: FreebuffAccountConnected, Label: "one@example.com"},
	}})
	got := read()
	if len(got.Accounts) != 2 || got.DefaultAccountID != first.ID {
		t.Fatalf("after report: %#v default=%q", got.Accounts, got.DefaultAccountID)
	}

	// A requested account cannot be the default; a connected one can.
	if w := freebuffCall(t, testHandler.SetFreebuffDefaultAccount, "POST", runtimeID, second.ID); w.Code != http.StatusConflict {
		t.Fatalf("default on an unconnected account: %d", w.Code)
	}
	daemonReports(freebuffReport{Accounts: []freebuffReportedAccount{
		{ID: second.ID, State: FreebuffAccountConnected, Label: "two@example.com"},
	}})
	if w := freebuffCall(t, testHandler.SetFreebuffDefaultAccount, "POST", runtimeID, second.ID); w.Code != http.StatusOK {
		t.Fatalf("set default: %d %s", w.Code, w.Body.String())
	}
	if got := read(); got.DefaultAccountID != second.ID {
		t.Fatalf("default = %q", got.DefaultAccountID)
	}

	// Retry puts a signed-out account back in the queue.
	if w := freebuffCall(t, testHandler.RetryFreebuffAccountLogin, "POST", runtimeID, first.ID); w.Code != http.StatusAccepted {
		t.Fatalf("retry: %d", w.Code)
	}
	if got := read(); got.Accounts[got.find(first.ID)].State != FreebuffAccountRequested {
		t.Fatalf("retry did not queue the sign-in: %#v", got.Accounts)
	}

	// Removing the default hands the default to the other connected account;
	// the daemon's next complete report drops the removed one.
	if w := freebuffCall(t, testHandler.RemoveFreebuffAccount, "DELETE", runtimeID, second.ID); w.Code != http.StatusAccepted {
		t.Fatalf("remove: %d", w.Code)
	}
	got = read()
	if got.Accounts[got.find(second.ID)].State != FreebuffAccountRemoving {
		t.Fatalf("remove did not mark the account: %#v", got.Accounts)
	}
	daemonReports(freebuffReport{Complete: true, Accounts: []freebuffReportedAccount{{ID: first.ID}}})
	got = read()
	if len(got.Accounts) != 1 || got.Accounts[0].ID != first.ID {
		t.Fatalf("after removal: %#v", got.Accounts)
	}

	// An account the daemon never started is dropped at once.
	var third FreebuffAccount
	w := freebuffCall(t, testHandler.AddFreebuffAccount, "POST", runtimeID, "")
	_ = json.Unmarshal(w.Body.Bytes(), &third)
	if w := freebuffCall(t, testHandler.RemoveFreebuffAccount, "DELETE", runtimeID, third.ID); w.Code != http.StatusAccepted {
		t.Fatalf("remove requested: %d", w.Code)
	}
	if got := read(); got.find(third.ID) >= 0 {
		t.Fatalf("a never-started account should vanish: %#v", got.Accounts)
	}

	if w := freebuffCall(t, testHandler.RemoveFreebuffAccount, "DELETE", runtimeID, "nope"); w.Code != http.StatusNotFound {
		t.Fatalf("remove unknown: %d", w.Code)
	}
}
