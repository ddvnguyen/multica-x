package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// planGateDaemon builds a daemon whose gate runs a shell script instead of the
// real binary: the default test suite never resolves user-installed tooling,
// and the gate's contract (exit status, result file) is what the daemon depends
// on, not Plannotator's internals.
func planGateDaemon(t *testing.T, handler http.HandlerFunc, cfg Config) *Daemon {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Daemon{
		client: NewClient(srv.URL),
		cfg:    cfg,
		logger: slog.Default(),
	}
}

// planGateFake writes a gate script. $2 is the plan file (argv is
// "annotate <plan> ...") and the result file is located by scanning for
// --result-file, so a case can fail on a missing plan before it writes
// anything.
func planGateFake(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gate is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "plannotator-fake")
	body := `#!/bin/sh
if [ ! -s "$2" ]; then
  echo "plan file missing or empty: $2" >&2
  exit 3
fi
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--result-file" ]; then out="$a"; fi
  prev="$a"
done
` + script
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake gate: %v", err)
	}
	return path
}

// parkedPlanServer answers the plan read with an open plan and captures the
// verdict it is sent. verdictStatus lets a case answer the post with a
// conflict; verdicts counts posts so a case can prove there was no retry.
func parkedPlanServer(t *testing.T, markdown string, verdictStatus int, gotPath *string, gotBody *map[string]any, posts *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/parked-plan"):
			*gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "ready",
				"plan_id":  "plan-1",
				"title":    "Plan",
				"revision": 4,
				"markdown": markdown,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/plan-verdict"):
			posts.Add(1)
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			*gotBody = body
			if verdictStatus != 0 && verdictStatus != http.StatusOK {
				w.WriteHeader(verdictStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "plan_closed"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"resume_task": "task-2"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestRunPlanGate_ApprovalPostsVerdictForTheOpenRevision(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	var posts atomic.Int32
	fake := planGateFake(t, `printf '%s' '{"decision":"approved"}' > "$out"
exit 0
`)
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan\n\nDo the thing.", 0, &gotPath, &gotBody, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if gotPath != "/api/daemon/tasks/task-1/parked-plan" {
		t.Fatalf("plan read path = %q, want the task-scoped parked-plan route", gotPath)
	}
	if posts.Load() != 1 {
		t.Fatalf("verdict posts = %d, want exactly 1", posts.Load())
	}
	if gotBody["verdict"] != "approve" {
		t.Fatalf("verdict = %v, want approve", gotBody["verdict"])
	}
	if gotBody["plan_id"] != "plan-1" {
		t.Fatalf("plan_id = %v, want plan-1", gotBody["plan_id"])
	}
	// The verdict is applied to the revision the gate read. Sending a stale
	// or missing revision would make the server's CAS meaningless.
	if rev, _ := gotBody["base_revision"].(float64); rev != 4 {
		t.Fatalf("base_revision = %v, want 4", gotBody["base_revision"])
	}
	if _, ok := gotBody["feedback"]; ok {
		t.Fatalf("an approval must not send feedback: %v", gotBody["feedback"])
	}
}

func TestRunPlanGate_AnnotationsBecomeTheFeedback(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	var posts atomic.Int32
	fake := planGateFake(t, `printf '%s' '{"decision":"annotated","feedback":"split the migration"}' > "$out"
exit 1
`)
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan", 0, &gotPath, &gotBody, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if gotBody["verdict"] != "changes_requested" {
		t.Fatalf("verdict = %v, want changes_requested", gotBody["verdict"])
	}
	if gotBody["feedback"] != "split the migration" {
		t.Fatalf("feedback = %v, want the reviewer's annotations", gotBody["feedback"])
	}
}

func TestRunPlanGate_DismissalLeavesThePlanParked(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	var posts atomic.Int32
	fake := planGateFake(t, `printf '%s' '{"decision":"dismissed"}' > "$out"
exit 1
`)
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan", 0, &gotPath, &gotBody, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if posts.Load() != 0 {
		t.Fatalf("a dismissal posted %d verdicts; closing the review is not a denial", posts.Load())
	}
}

func TestRunPlanGate_GateFailureSendsNoVerdict(t *testing.T) {
	var gotBody map[string]any
	var posts atomic.Int32
	fake := planGateFake(t, `exit 2
`)
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan", 0, new(string), &gotBody, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if posts.Load() != 0 {
		t.Fatalf("a broken gate posted %d verdicts; it must never read as a denial", posts.Load())
	}
}

func TestRunPlanGate_ConflictIsNotRetried(t *testing.T) {
	var gotBody map[string]any
	var posts atomic.Int32
	fake := planGateFake(t, `printf '%s' '{"decision":"approved"}' > "$out"
exit 0
`)
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan", http.StatusConflict, new(string), &gotBody, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	// The panel (or an answer run) decided first. One losing post is the whole
	// cost: retrying would keep trying to overwrite someone else's choice.
	if posts.Load() != 1 {
		t.Fatalf("verdict posts = %d after a conflict, want exactly 1", posts.Load())
	}
}

func TestRunPlanGate_NoOpenPlanNeverRunsTheGate(t *testing.T) {
	var posts atomic.Int32
	marker := filepath.Join(t.TempDir(), "gate-ran")
	fake := planGateFake(t, `touch `+marker+`
exit 0
`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "none"})
	}))
	t.Cleanup(srv.Close)
	d := &Daemon{client: NewClient(srv.URL), cfg: Config{PlanGateEnabled: true, PlanGateBinary: fake}, logger: slog.Default()}

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the gate ran with no plan to review")
	}
	if posts.Load() != 0 {
		t.Fatalf("an empty plan read posted %d verdicts", posts.Load())
	}
}

func TestStartPlanGate_DisabledDoesNotReadTheServer(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	d := &Daemon{client: NewClient(srv.URL), cfg: Config{PlanGateEnabled: false}, logger: slog.Default()}

	d.startPlanGate("task-1", slog.Default())

	if reads != 0 {
		t.Fatalf("a disabled gate read the server %d times", reads)
	}
}

func TestPlanGateConflictCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"conflict with code", &requestError{StatusCode: http.StatusConflict, Body: `{"error":"plan_closed"}`}, "plan_closed"},
		{"conflict without a body", &requestError{StatusCode: http.StatusConflict}, "conflict"},
		{"conflict with unknown shape", &requestError{StatusCode: http.StatusConflict, Body: "not json"}, "conflict"},
		{"server error is not a conflict", &requestError{StatusCode: http.StatusInternalServerError, Body: `{"error":"boom"}`}, ""},
		{"other error", io.ErrUnexpectedEOF, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planGateConflictCode(tc.err); got != tc.want {
				t.Fatalf("planGateConflictCode(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestRunPlanGate_CancelKillsTheGate: the gate is a child process bounded by
// the context it was started with, so a cancelled context (daemon shutdown,
// restart, self-update) must end the review instead of leaving a reviewer's
// tab and a temp dir behind. A killed gate is not a verdict: nothing is
// posted, and the plan stays parked for the panel.
func TestRunPlanGate_CancelKillsTheGate(t *testing.T) {
	started := filepath.Join(t.TempDir(), "gate-started")
	fake := planGateFake(t, "touch "+started+"\nsleep 30\nexit 0\n")
	var posts atomic.Int32
	var body map[string]any
	d := planGateDaemon(t, parkedPlanServer(t, "## Plan", 0, new(string), &body, &posts),
		Config{PlanGateEnabled: true, PlanGateBinary: fake})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.runPlanGate(ctx, "task-1", slog.Default())
		close(done)
	}()

	waitForPath(t, started)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled gate did not return: the review process outlives the daemon")
	}
	if posts.Load() != 0 {
		t.Fatalf("a cancelled gate posted %d verdicts", posts.Load())
	}
}

// TestStartPlanGate_UsesTheDaemonLifecycleContext pins where the gate's
// cancellation comes from. A daemon whose lifecycle context is already done
// must not start a review at all — with context.Background() the read would
// still reach the server and open one.
func TestStartPlanGate_UsesTheDaemonLifecycleContext(t *testing.T) {
	var posts atomic.Int32
	var body map[string]any
	hit := make(chan struct{}, 1)
	handler := func(w http.ResponseWriter, r *http.Request) {
		select {
		case hit <- struct{}{}:
		default:
		}
		parkedPlanServer(t, "## Plan", 0, new(string), &body, &posts)(w, r)
	}
	d := planGateDaemon(t, handler, Config{PlanGateEnabled: true, PlanGateBinary: planGateFake(t, "exit 0\n")})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.setLifecycleCtx(ctx)

	d.startPlanGate("task-1", slog.Default())

	select {
	case <-hit:
		t.Fatal("a shut-down daemon still opened a review: the gate is not bounded by its lifecycle")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRunPlanGate_SendsTheSubmittedRevision: ReviewPlan fences on
// submitted_revision, not on the live edit counter. Sending the wrong one
// turns a valid approval into a 409 the reviewer never sees explained.
func TestRunPlanGate_SendsTheSubmittedRevision(t *testing.T) {
	var body map[string]any
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":             "ready",
				"plan_id":            "plan-1",
				"revision":           9,
				"submitted_revision": 4,
				"markdown":           "## Plan",
			})
			return
		}
		posts.Add(1)
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.NewEncoder(w).Encode(map[string]any{"resume_task": "task-2"})
	}))
	t.Cleanup(srv.Close)
	fake := planGateFake(t, "printf '%s' '{\"decision\":\"approved\"}' > \"$out\"\nexit 0\n")
	d := &Daemon{client: NewClient(srv.URL), cfg: Config{PlanGateEnabled: true, PlanGateBinary: fake}, logger: slog.Default()}

	d.runPlanGate(context.Background(), "task-1", slog.Default())

	if posts.Load() != 1 {
		t.Fatalf("verdict posts = %d, want 1", posts.Load())
	}
	if rev, _ := body["base_revision"].(float64); rev != 4 {
		t.Fatalf("base_revision = %v, want the submitted revision 4", body["base_revision"])
	}
}

// waitForPath blocks until the gate has written its start marker.
func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gate never started: %s was not created", path)
}
