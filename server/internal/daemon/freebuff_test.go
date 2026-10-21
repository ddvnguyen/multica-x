package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// freebuffFixture stands up a Daemon whose Freebuff reports are captured,
// plus a fake wrapper that behaves like the real one: it prints one JSON
// line with the login URL, then one with the outcome.
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
	d.profileLaunchSpecs = make(map[string]profileLaunchSpec)
	fx.daemon = d
	return fx
}

func (fx *freebuffFixture) snapshot() []map[string]any {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]map[string]any(nil), fx.reports...)
}

// fakeFreebuffWrapper writes an executable that mimics the wrapper's two
// modes: `--status --json` prints an account snapshot, `login --json` prints
// the login URL and then the outcome.
func fakeFreebuffWrapper(t *testing.T, statusJSON string, loginLines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codebuddy-freebuff")
	script := "#!/bin/sh\n"
	script += "if [ \"$1\" = \"--status\" ]; then\n"
	script += "  cat <<'EOF'\n" + statusJSON + "\nEOF\n  exit 0\nfi\n"
	script += "cat <<'EOF'\n"
	for _, line := range loginLines {
		script += line + "\n"
	}
	script += "EOF\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake wrapper: %v", err)
	}
	return path
}

func freebuffRuntime(path string) Runtime {
	return Runtime{ID: "rt-freebuff", Provider: "codebuddy", ProfileID: "prof-fb"}
}

// TestReportFreebuffStatus_SendsAccountSnapshot pins the account report:
// the daemon runs the wrapper's `--status --json` and hands the snapshot to
// the server unchanged.
func TestReportFreebuffStatus_SendsAccountSnapshot(t *testing.T) {
	fx := newFreebuffFixture(t)
	d := fx.daemon

	wrapper := fakeFreebuffWrapper(t,
		`{"status":"active","remainingMs":120000,"credit":{"dailyRemaining":4,"dailyLimit":5,"balance":21},"prices":{"z-ai/glm-5.3-flash":2}}`,
		nil,
	)
	d.profileLaunchSpecs["prof-fb"] = profileLaunchSpec{path: wrapper, commandName: freebuffCommandName}
	rt := freebuffRuntime(wrapper)
	d.runtimeIndex[rt.ID] = rt

	d.reportFreebuffStatus(context.Background(), rt)

	reports := fx.snapshot()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	status, ok := reports[0]["status"].(map[string]any)
	if !ok {
		t.Fatalf("report has no status object: %#v", reports[0])
	}
	if status["status"] != "active" {
		t.Fatalf("status state = %v, want active", status["status"])
	}
	if status["remainingMs"] != float64(120000) {
		t.Fatalf("remainingMs = %v, want 120000", status["remainingMs"])
	}
	credit, ok := status["credit"].(map[string]any)
	if !ok {
		t.Fatalf("status has no credit object: %#v", status)
	}
	if credit["dailyRemaining"] != float64(4) {
		t.Fatalf("dailyRemaining = %v, want 4", credit["dailyRemaining"])
	}
}

// TestHandleFreebuffConnect_ReportsURLThenOutcome pins the browser login:
// the daemon forwards the one-time URL as soon as the wrapper has it, and the
// outcome when the browser confirms.
func TestHandleFreebuffConnect_ReportsURLThenOutcome(t *testing.T) {
	fx := newFreebuffFixture(t)
	d := fx.daemon

	wrapper := fakeFreebuffWrapper(t, "{}", []string{
		`{"status":"awaiting_login","loginUrl":"https://freebuff.com/login?auth_code=abc"}`,
		`{"status":"connected","account":"owner@example.com"}`,
	})
	d.profileLaunchSpecs["prof-fb"] = profileLaunchSpec{path: wrapper, commandName: freebuffCommandName}
	rt := freebuffRuntime(wrapper)
	d.runtimeIndex[rt.ID] = rt

	d.handleFreebuffConnect(context.Background(), rt)

	reports := fx.snapshot()
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports (url, outcome), got %d: %#v", len(reports), reports)
	}
	first := reports[0]["connect"].(map[string]any)
	if first["state"] != "awaiting_login" {
		t.Fatalf("first state = %v, want awaiting_login", first["state"])
	}
	if first["login_url"] != "https://freebuff.com/login?auth_code=abc" {
		t.Fatalf("login_url = %v", first["login_url"])
	}
	second := reports[1]["connect"].(map[string]any)
	if second["state"] != "connected" {
		t.Fatalf("second state = %v, want connected", second["state"])
	}
	if second["account"] != "owner@example.com" {
		t.Fatalf("account = %v", second["account"])
	}
}

// TestFreebuffRuntimes_OnlyFreebuffProfiles pins which runtimes the daemon
// treats as Freebuff runtimes: the ones whose profile command is the
// wrapper. A runtime on the same protocol family but a different command
// must not be picked up — it would run someone else's CLI with the
// wrapper's arguments.
func TestFreebuffRuntimes_OnlyFreebuffProfiles(t *testing.T) {
	fx := newFreebuffFixture(t)
	d := fx.daemon

	d.profileLaunchSpecs["prof-fb"] = profileLaunchSpec{path: "/usr/local/bin/codebuddy-freebuff", commandName: freebuffCommandName}
	d.profileLaunchSpecs["prof-other"] = profileLaunchSpec{path: "/usr/local/bin/codebuddy", commandName: "codebuddy"}
	d.runtimeIndex["rt-freebuff"] = Runtime{ID: "rt-freebuff", Provider: "codebuddy", ProfileID: "prof-fb"}
	d.runtimeIndex["rt-plain"] = Runtime{ID: "rt-plain", Provider: "codebuddy", ProfileID: "prof-other"}
	d.runtimeIndex["rt-builtin"] = Runtime{ID: "rt-builtin", Provider: "codebuddy"}

	runtimes := d.freebuffRuntimes()
	if len(runtimes) != 1 {
		t.Fatalf("expected 1 freebuff runtime, got %d: %#v", len(runtimes), runtimes)
	}
	if runtimes[0].ID != "rt-freebuff" {
		t.Fatalf("freebuff runtime = %s, want rt-freebuff", runtimes[0].ID)
	}
}
