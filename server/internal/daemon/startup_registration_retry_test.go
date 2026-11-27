package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// startupRetryBackend is a throwaway HTTP backend for startup registration.
// It never touches the live service. Status hooks are keyed by call number so
// a test can simulate "refusal/5xx, then recovery" deterministically.
type startupRetryBackend struct {
	mu             sync.Mutex
	workspaces     []WorkspaceInfo
	listStatus     func(call int) int
	registerStatus func(workspaceID string, call int) int
	listCalls      int
	registerCalls  int
	server         *httptest.Server
}

func newStartupRetryBackend(t *testing.T) *startupRetryBackend {
	t.Helper()
	b := &startupRetryBackend{
		workspaces: []WorkspaceInfo{{ID: "ws-1", Name: "one"}},
	}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tokens/current/renew":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"renewed":false}`))
		case r.URL.Path == "/api/daemon/workspaces":
			b.mu.Lock()
			b.listCalls++
			call := b.listCalls
			hook := b.listStatus
			workspaces := append([]WorkspaceInfo(nil), b.workspaces...)
			b.mu.Unlock()
			if hook != nil {
				if status := hook(call); status != 0 {
					w.WriteHeader(status)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(workspaces)
		case r.URL.Path == "/api/daemon/register":
			var body struct {
				WorkspaceID string `json:"workspace_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.mu.Lock()
			b.registerCalls++
			call := b.registerCalls
			hook := b.registerStatus
			b.mu.Unlock()
			if hook != nil {
				if status := hook(body.WorkspaceID, call); status != 0 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"injected startup failure"}`))
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RegisterResponse{
				Runtimes: []Runtime{{ID: "rt-" + body.WorkspaceID, Name: "codex", Provider: "codex", Status: "online"}},
			})
		case strings.HasSuffix(r.URL.Path, "/runtime-profiles"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(RuntimeProfilesResponse{WorkspaceID: "ws-1"})
		case strings.HasSuffix(r.URL.Path, "/recover-orphans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(b.server.Close)
	return b
}

func (b *startupRetryBackend) daemon(t *testing.T) *Daemon {
	t.Helper()
	d := freshDaemon(b.server.URL)
	d.profileLaunchSpecs = make(map[string]profileLaunchSpec)
	// A built-in runtime so the registration payload is non-empty; without it
	// registration fails with the config error ErrNoRuntimesToRegister instead
	// of the HTTP status the test injects.
	d.cfg.Agents = map[string]AgentEntry{"codex": {Path: "/fake/codex"}}
	restore := stubAgentVersion(t)
	t.Cleanup(restore)
	return d
}

func (b *startupRetryBackend) counts() (list, register int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listCalls, b.registerCalls
}

// fastStartupRetries makes the retry wait instant and records every attempt
// without letting tests run in parallel (the patched vars are package-scoped).
func fastStartupRetries(t *testing.T) *atomic.Int32 {
	t.Helper()
	origDelay, origSleep := boundedStartupRetryDelay, startupRetrySleep
	attempts := &atomic.Int32{}
	boundedStartupRetryDelay = func(attempt int) time.Duration {
		attempts.Add(1)
		return time.Millisecond
	}
	startupRetrySleep = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
			return nil
		}
	}
	t.Cleanup(func() { boundedStartupRetryDelay, startupRetrySleep = origDelay, origSleep })
	return attempts
}

// Refusal and 5xx/429, then recovery: registration succeeds without the daemon
// being restarted (no error leaves preflight, so Run never exits). Readiness
// stays false throughout — Run flips d.ready only after preflight returns.
func TestStartupRetry_TransientFailuresThenRecover(t *testing.T) {
	b := newStartupRetryBackend(t)
	b.registerStatus = func(_ string, call int) int {
		switch call {
		case 1:
			return http.StatusServiceUnavailable
		case 2:
			return http.StatusTooManyRequests
		default:
			return 0
		}
	}
	// Simulate connection refusal on the first ListWorkspaces, then recovery.
	b.listStatus = func(call int) int {
		if call == 1 {
			return http.StatusBadGateway
		}
		return 0
	}
	d := b.daemon(t)
	_ = fastStartupRetries(t)

	if err := d.syncWorkspacesForStartup(context.Background()); err != nil {
		t.Fatalf("syncWorkspacesForStartup: %v, want recovery", err)
	}
	d.mu.Lock()
	_, registered := d.workspaces["ws-1"]
	d.mu.Unlock()
	if !registered {
		t.Fatal("workspace was not registered after recovery")
	}
	_, registerCalls := b.counts()
	if registerCalls < 2 {
		t.Fatalf("Register calls = %d, want retries before recovery", registerCalls)
	}
	if d.ready.Load() {
		t.Fatal("ready flipped during preflight; tasks could be claimed before registration succeeded")
	}
}

// A permanent auth error exits visibly: no retry, and the error still wraps
// errNoWorkspaceRuntimesRegistered so the existing DSH exception keeps working.
func TestStartupRetry_PermanentAuthErrorFailsFast(t *testing.T) {
	b := newStartupRetryBackend(t)
	b.registerStatus = func(_ string, _ int) int { return http.StatusUnauthorized }
	d := b.daemon(t)
	attempts := fastStartupRetries(t)

	err := d.syncWorkspacesForStartup(context.Background())
	if err == nil {
		t.Fatal("expected a permanent auth error to fail startup")
	}
	if !errors.Is(err, errNoWorkspaceRuntimesRegistered) {
		t.Errorf("error = %v, want wrapped errNoWorkspaceRuntimesRegistered", err)
	}
	if got := startupErrorClass(err); got != "http_401" {
		t.Errorf("error class = %q, want http_401", got)
	}
	if got := attempts.Load(); got != 0 {
		t.Errorf("slept %d times for a permanent error, want 0", got)
	}
	_, registerCalls := b.counts()
	if registerCalls != 1 {
		t.Errorf("Register calls = %d, want exactly 1 (no retry)", registerCalls)
	}
}

// Context cancellation stops the retry promptly (systemd stop during an outage).
func TestStartupRetry_ContextCancellationStopsRetry(t *testing.T) {
	b := newStartupRetryBackend(t)
	b.registerStatus = func(_ string, _ int) int { return http.StatusInternalServerError }
	d := b.daemon(t)

	origDelay, origSleep := boundedStartupRetryDelay, startupRetrySleep
	t.Cleanup(func() { boundedStartupRetryDelay, startupRetrySleep = origDelay, origSleep })
	boundedStartupRetryDelay = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startupRetrySleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	err := d.syncWorkspacesForStartup(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	_, registerCalls := b.counts()
	if registerCalls != 1 {
		t.Errorf("Register calls = %d, want 1 (cancelled before retry)", registerCalls)
	}
}

// Partial success proceeds: a failed workspace is left to the periodic sync,
// and startup does not retry the whole round.
func TestStartupRetry_PartialSuccessProceeds(t *testing.T) {
	b := newStartupRetryBackend(t)
	b.workspaces = []WorkspaceInfo{{ID: "ws-bad", Name: "bad"}, {ID: "ws-ok", Name: "ok"}}
	b.registerStatus = func(workspaceID string, _ int) int {
		if workspaceID == "ws-bad" {
			return http.StatusInternalServerError
		}
		return 0
	}
	d := b.daemon(t)
	_ = fastStartupRetries(t)

	if err := d.syncWorkspacesForStartup(context.Background()); err != nil {
		t.Fatalf("partial success should proceed, got %v", err)
	}
	d.mu.Lock()
	_, okRegistered := d.workspaces["ws-ok"]
	_, badRegistered := d.workspaces["ws-bad"]
	d.mu.Unlock()
	if !okRegistered || badRegistered {
		t.Fatalf("workspaces: ws-ok registered=%v, ws-bad registered=%v; want true/false", okRegistered, badRegistered)
	}
	_, registerCalls := b.counts()
	if registerCalls != 2 {
		t.Errorf("Register calls = %d, want 2 (one per workspace, no retry)", registerCalls)
	}
}

// The DSH bootstrap exception is preserved: while a profile install is in
// flight, a round that registers nothing still lets startup proceed.
func TestStartupRetry_DSHBootstrapStillProceeds(t *testing.T) {
	b := newStartupRetryBackend(t)
	b.registerStatus = func(_ string, _ int) int { return http.StatusServiceUnavailable }
	d := b.daemon(t)
	_ = fastStartupRetries(t)
	d.dshInstallInFlight.Store(true)

	if err := d.preflightAuth(context.Background()); err != nil {
		t.Fatalf("preflightAuth with DSH install in flight = %v, want nil", err)
	}
}

// A config error (no runtimes resolvable) is permanent even though it is not
// an HTTP response: it never resolves on retry and must fail visibly.
func TestStartupRetry_NoRuntimesIsPermanent(t *testing.T) {
	err := fmt.Errorf("register runtimes: %w", ErrNoRuntimesToRegister)
	if isRetryableStartupError(err) {
		t.Fatal("ErrNoRuntimesToRegister classified as retryable")
	}
	if got := startupErrorClass(err); got != "no_runtimes" {
		t.Errorf("error class = %q, want no_runtimes", got)
	}
}

func TestIsRetryableStartupError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transport", errors.New("connection refused"), true},
		{"deadline", context.DeadlineExceeded, true},
		{"5xx", &requestError{StatusCode: 503}, true},
		{"408", &requestError{StatusCode: 408}, true},
		{"429", &requestError{StatusCode: 429}, true},
		{"401", &requestError{StatusCode: 401}, false},
		{"403", &requestError{StatusCode: 403}, false},
		{"400", &requestError{StatusCode: 400}, false},
		{"invalid body", errInvalidResponseBody, false},
		{"no runtimes", ErrNoRuntimesToRegister, false},
		{
			"all transient aggregate",
			&startupRegistrationError{count: 2, failures: []workspaceRegistrationFailure{
				{workspaceID: "ws-1", err: &requestError{StatusCode: 503}},
				{workspaceID: "ws-2", err: errors.New("connection refused")},
			}},
			true,
		},
		{
			"mixed aggregate fails visibly",
			&startupRegistrationError{count: 2, failures: []workspaceRegistrationFailure{
				{workspaceID: "ws-1", err: &requestError{StatusCode: 503}},
				{workspaceID: "ws-2", err: &requestError{StatusCode: 401}},
			}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableStartupError(tt.err); got != tt.want {
				t.Errorf("isRetryableStartupError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestStartupErrorClassAndWorkspaces(t *testing.T) {
	err := &startupRegistrationError{count: 2, failures: []workspaceRegistrationFailure{
		{workspaceID: "ws-1", err: &requestError{StatusCode: 503, Body: "secret-token-in-body"}},
		{workspaceID: "ws-2", err: &requestError{StatusCode: 503}},
	}}
	if got := startupErrorClass(err); got != "http_5xx" {
		t.Errorf("class = %q, want http_5xx", got)
	}
	got := startupErrorWorkspaces(err)
	if len(got) != 2 || got[0] != "ws-1" || got[1] != "ws-2" {
		t.Errorf("workspaces = %v, want [ws-1 ws-2]", got)
	}
	mixed := &startupRegistrationError{count: 2, failures: []workspaceRegistrationFailure{
		{workspaceID: "ws-1", err: &requestError{StatusCode: 503}},
		{workspaceID: "ws-2", err: &requestError{StatusCode: 401}},
	}}
	if got := startupErrorClass(mixed); got != "mixed" {
		t.Errorf("mixed class = %q, want mixed", got)
	}
}

// Backoff is bounded and jittered: every delay stays within half the cap and
// the cap itself, and the cap is hit for larger attempts.
func TestBoundedStartupRetryDelay(t *testing.T) {
	for attempt := 1; attempt <= 12; attempt++ {
		delay := boundedStartupRetryDelay(attempt)
		if delay < startupRetryBaseDelay/2 || delay > startupRetryMaxDelay {
			t.Errorf("attempt %d delay = %v, want [%v, %v]", attempt, delay, startupRetryBaseDelay/2, startupRetryMaxDelay)
		}
	}
	if got := boundedStartupRetryDelay(10); got > startupRetryMaxDelay {
		t.Errorf("attempt 10 delay = %v, exceeds cap %v", got, startupRetryMaxDelay)
	}
}
