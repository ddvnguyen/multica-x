package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type detachedReport struct {
	taskID string
	result agent.Result
}

type detachedRecoverFixture struct {
	mu         sync.Mutex
	recovered  []string
	watched    []string
	reports    chan detachedReport
	outcomes   map[string]agent.DshDetachedOutcome
	recoverErr map[string]error
	watchFn    func(ctx context.Context, workDir, taskID string) (agent.Result, error)
}

func newDetachedRecoverFixture() *detachedRecoverFixture {
	return &detachedRecoverFixture{
		reports:    make(chan detachedReport, 8),
		outcomes:   map[string]agent.DshDetachedOutcome{},
		recoverErr: map[string]error{},
	}
}

func (f *detachedRecoverFixture) run(ctx context.Context, tasks []RunningDetachedTask) (reported, watching, skipped int) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return recoverDetachedTasks(ctx, tasks,
		func(workDir, taskID string) (agent.DshDetachedOutcome, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.recovered = append(f.recovered, taskID)
			return f.outcomes[taskID], f.recoverErr[taskID]
		},
		func(ctx context.Context, workDir, taskID string) (agent.Result, error) {
			f.mu.Lock()
			f.watched = append(f.watched, taskID)
			fn := f.watchFn
			f.mu.Unlock()
			return fn(ctx, workDir, taskID)
		},
		func(_ context.Context, taskID string, result agent.Result) {
			f.reports <- detachedReport{taskID, result}
		},
		logger)
}

func (f *detachedRecoverFixture) wantReport(t *testing.T) detachedReport {
	t.Helper()
	select {
	case r := <-f.reports:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a report")
		return detachedReport{}
	}
}

func (f *detachedRecoverFixture) wantNoReport(t *testing.T) {
	t.Helper()
	select {
	case r := <-f.reports:
		t.Fatalf("unexpected report: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRecoverDetachedTasks_FinishedIsReportedOnce(t *testing.T) {
	f := newDetachedRecoverFixture()
	want := agent.Result{Status: "completed", Output: "done", SessionID: "s-1"}
	f.outcomes["t-1"] = agent.DshDetachedOutcome{Finished: true, Result: want}

	reported, watching, skipped := f.run(context.Background(), []RunningDetachedTask{{TaskID: "t-1", WorkDir: "/w"}})
	if reported != 1 || watching != 0 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/0/0", reported, watching, skipped)
	}
	got := f.wantReport(t)
	if got.taskID != "t-1" || got.result.Status != want.Status || got.result.Output != want.Output || got.result.SessionID != want.SessionID {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
	f.wantNoReport(t)
	if len(f.watched) != 0 {
		t.Fatalf("finished run must not be watched: %v", f.watched)
	}
}

func TestRecoverDetachedTasks_LostIsReportedFailed(t *testing.T) {
	f := newDetachedRecoverFixture()
	f.outcomes["t-1"] = agent.DshDetachedOutcome{Lost: true, Result: agent.Result{Status: "failed", Error: "scope gone"}}

	reported, _, _ := f.run(context.Background(), []RunningDetachedTask{{TaskID: "t-1", WorkDir: "/w"}})
	if reported != 1 {
		t.Fatalf("reported = %d, want 1", reported)
	}
	got := f.wantReport(t)
	if got.result.Status != "failed" || got.result.Error != "scope gone" {
		t.Fatalf("report = %+v", got)
	}
}

func TestRecoverDetachedTasks_StillRunningIsWatchedThenReported(t *testing.T) {
	f := newDetachedRecoverFixture()
	release := make(chan struct{})
	want := agent.Result{Status: "completed", Output: "late"}
	f.watchFn = func(ctx context.Context, _, _ string) (agent.Result, error) {
		<-release
		return want, nil
	}

	reported, watching, skipped := f.run(context.Background(), []RunningDetachedTask{{TaskID: "t-1", WorkDir: "/w"}})
	if reported != 0 || watching != 1 || skipped != 0 {
		t.Fatalf("counts = %d/%d/%d, want 0/1/0", reported, watching, skipped)
	}
	f.wantNoReport(t) // not reported while the run is still going
	close(release)
	got := f.wantReport(t)
	if got.taskID != "t-1" || got.result.Output != "late" {
		t.Fatalf("report = %+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.watched) != 1 || f.watched[0] != "t-1" {
		t.Fatalf("watched = %v", f.watched)
	}
}

func TestRecoverDetachedTasks_EmptyWorkDirIsSkipped(t *testing.T) {
	f := newDetachedRecoverFixture()
	reported, watching, skipped := f.run(context.Background(), []RunningDetachedTask{{TaskID: "t-1", WorkDir: "  "}})
	if reported != 0 || watching != 0 || skipped != 1 {
		t.Fatalf("counts = %d/%d/%d, want 0/0/1", reported, watching, skipped)
	}
	if len(f.recovered) != 0 {
		t.Fatalf("recoverRun must not be called without a work_dir: %v", f.recovered)
	}
	f.wantNoReport(t)
}

func TestRecoverDetachedTasks_RecoverErrorIsSkipped(t *testing.T) {
	f := newDetachedRecoverFixture()
	f.recoverErr["t-1"] = errors.New("run dir missing")
	reported, watching, skipped := f.run(context.Background(), []RunningDetachedTask{{TaskID: "t-1", WorkDir: "/w"}})
	if reported != 0 || watching != 0 || skipped != 1 {
		t.Fatalf("counts = %d/%d/%d, want 0/0/1", reported, watching, skipped)
	}
	f.wantNoReport(t)
	if len(f.watched) != 0 {
		t.Fatalf("unreadable run must not be watched: %v", f.watched)
	}
}

func TestRecoverDetachedTasks_WatchContextErrorIsNotReported(t *testing.T) {
	f := newDetachedRecoverFixture()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	f.watchFn = func(ctx context.Context, _, _ string) (agent.Result, error) {
		close(started)
		<-ctx.Done()
		defer close(done)
		return agent.Result{}, ctx.Err()
	}

	_, watching, _ := f.run(ctx, []RunningDetachedTask{{TaskID: "t-1", WorkDir: "/w"}})
	if watching != 1 {
		t.Fatalf("watching = %d, want 1", watching)
	}
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not return")
	}
	f.wantNoReport(t)
}

func TestDetachedTaskResult(t *testing.T) {
	if got := detachedTaskResult(agent.Result{Status: "completed", Output: "ok", SessionID: "s"}); got.Status != "completed" || got.Comment != "ok" || got.SessionID != "s" {
		t.Fatalf("completed = %+v", got)
	}
	if got := detachedTaskResult(agent.Result{Status: "failed", Error: "boom", Output: "x"}); got.Status != "failed" || got.Comment != "boom" {
		t.Fatalf("failed = %+v", got)
	}
	if got := detachedTaskResult(agent.Result{Error: "lost"}); got.Status != "failed" || got.Comment != "lost" {
		t.Fatalf("empty status = %+v", got)
	}
}
