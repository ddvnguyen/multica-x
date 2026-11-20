package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// writeArmedSession lays down a session directory with an arm record the way a
// previous daemon's Arm would have.
func writeArmedSession(t *testing.T, root, workDir, sessionID string, endedAt time.Time) {
	t.Helper()
	rec := dshArmRecord{
		TaskID: "task-" + sessionID, WorkDir: workDir, Model: "deepseek/deepseek-v4-pro",
		EndedAt: endedAt, FirstPromptTokens: 20000, LastPromptTokens: 60000,
	}
	if err := writeDshArmRecord(dshArmRecordPath(root, workDir, sessionID), rec); err != nil {
		t.Fatal(err)
	}
}

// TestRearmDshSummariesRestoresWarmSessionsOnly covers one root with every
// session shape a restart can meet.
func TestRearmDshSummariesRestoresWarmSessionsOnly(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())

	writeArmedSession(t, root, workDir, "warm", now.Add(-30*time.Second))
	writeArmedSession(t, root, workDir, "summarized", now.Add(-30*time.Second))
	if err := writeDshSummarySidecar(root, workDir, "summarized", dshSessionSummaryRecord{Summary: "kept"}); err != nil {
		t.Fatal(err)
	}
	writeArmedSession(t, root, workDir, "cold", now.Add(-91*time.Second))
	if err := os.MkdirAll(dshSessionStoreDir(root, workDir, "unarmed"), 0o700); err != nil {
		t.Fatal(err)
	}

	var records []dshSummaryRunRowRecord
	var summarized []string
	var requests []dshSummaryRequest
	got := rearmDshSummaries(clock, []string{root}, func(rec dshArmRecord, sessionID, storeRoot string) (dshSummaryRequest, bool) {
		req := dshSummaryRequest{
			TaskID: rec.TaskID, SessionID: sessionID, WorkDir: rec.WorkDir, StoreRoot: storeRoot,
			Model: rec.Model, FirstPromptTokens: rec.FirstPromptTokens,
			LastPromptTokens: rec.LastPromptTokens, EndedAt: rec.EndedAt,
			Summarize: func(_ context.Context, id, model, _ string, _ int64) (*agent.DshSessionSummary, error) {
				summarized = append(summarized, id)
				return &agent.DshSessionSummary{SessionID: id, Summary: "rebuilt", Tokens: 100, Model: model}, nil
			},
			RecordDecision: func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) },
		}
		requests = append(requests, req)
		return req, true
	})

	if got != 1 || len(scheduler.fires) != 1 {
		t.Fatalf("re-armed %d (fires %d), want exactly the warm session", got, len(scheduler.fires))
	}
	if len(requests) != 1 || requests[0].SessionID != "warm" {
		t.Fatalf("requests built for %#v, want only the warm session", requests)
	}
	if delay := scheduler.delay(t, 0); delay != 60*time.Second {
		t.Fatalf("delay = %s, want 60s (ended 30s ago, summarize point at 90s)", delay)
	}
	for _, id := range []string{"summarized", "cold"} {
		if _, ok := readArmRecordFile(t, root, workDir, id); ok {
			t.Fatalf("arm.json of %q was not removed", id)
		}
	}
	if got := readSummarySidecar(t, root, workDir, "summarized"); got.Summary != "kept" {
		t.Fatalf("existing summary changed: %#v", got)
	}

	*now = now.Add(60 * time.Second)
	scheduler.fire(t, 0)
	if len(summarized) != 1 || summarized[0] != "warm" {
		t.Fatalf("summarized %v, want [warm]", summarized)
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonProceed || records[0].TokensAboveInit != 40000 {
		t.Fatalf("decision = %#v, want summarized with 40000 tokens above init", records)
	}
	if records[0].IdleSeconds != 90 {
		t.Fatalf("idle = %ds, want 90 (measured from the original run end)", records[0].IdleSeconds)
	}
	if sidecar := readSummarySidecar(t, root, workDir, "warm"); sidecar.Summary != "rebuilt" {
		t.Fatalf("sidecar = %#v", sidecar)
	}
	if _, ok := readArmRecordFile(t, root, workDir, "warm"); ok {
		t.Fatal("arm.json of the warm session survived its fire")
	}
}

func TestRearmDshSummariesLeavesSessionWhenNoRequest(t *testing.T) {
	root, workDir := t.TempDir(), t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	writeArmedSession(t, root, workDir, "warm", *now)

	got := rearmDshSummaries(clock, []string{root}, func(dshArmRecord, string, string) (dshSummaryRequest, bool) {
		return dshSummaryRequest{}, false
	})
	if got != 0 || len(scheduler.fires) != 0 {
		t.Fatalf("re-armed %d sessions without a request", got)
	}
	if _, ok := readArmRecordFile(t, root, workDir, "warm"); !ok {
		t.Fatal("arm.json removed although the session was only skipped")
	}
}

func TestDshTaskSessionRootsFindsRuntimeAgentDirs(t *testing.T) {
	profileDir := t.TempDir()
	want := filepath.Join(profileDir, dshTaskSessionsDirName, "rt-1", "agent-1")
	if err := os.MkdirAll(want, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file at the agent level is not a root.
	if err := os.WriteFile(filepath.Join(profileDir, dshTaskSessionsDirName, "rt-1", "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	roots := dshTaskSessionRoots(profileDir)
	if len(roots) != 1 || roots[0] != want {
		t.Fatalf("roots = %v, want [%s]", roots, want)
	}
	if got := dshTaskSessionRoots(t.TempDir()); len(got) != 0 {
		t.Fatalf("roots of a profile without dsh sessions = %v", got)
	}
}
