package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dshsession"
)

// S3-B2 (M-138) acceptance: the warm / cold / unrelated / no-summary branches
// of the seeded-session decision, driven by a fake clock and a fake summary
// store — no dsh process, no wall clock, no filesystem.

// testSessionStartConfig is the production default shape with a 1 h TTL and a
// 15 min margin, so "warm" means "ended less than 45 minutes ago".
func testSessionStartConfig() DshSummaryConfig {
	return DshSummaryConfig{
		Enabled:       true,
		TTL:           time.Hour,
		Margin:        15 * time.Minute,
		SummaryTokens: DefaultDshSummaryTokens,
	}
}

// summaryStore is the fake sidecar reader: session id → record.
type summaryStore map[string]dshSessionSummaryRecord

func (s summaryStore) read(candidate dshSessionCandidate) (dshSessionSummaryRecord, bool) {
	record, ok := s[candidate.SessionID]
	return record, ok
}

func TestDecideDshSessionStart(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	const priorID = "sess-prior"
	priorEnded := now.Add(-2 * time.Hour)
	priorEndedAt := priorEnded.Format(time.RFC3339)

	// A summary made while the session was still warm, after the run ended.
	freshSummary := summaryStore{priorID: {
		Summary:       "We shipped the parser and left the tests half-written.",
		Tokens:        14,
		MadeAt:        priorEnded.Add(45 * time.Minute),
		ParentSession: priorID,
	}}
	// A summary written before the session ended describes history the session
	// has already moved past.
	staleSummary := summaryStore{priorID: {
		Summary:       "Older digest.",
		MadeAt:        priorEnded.Add(-time.Minute),
		ParentSession: priorID,
	}}

	cases := []struct {
		name          string
		in            dshSessionStartInput
		store         summaryStore
		wantVerdict   string
		wantResume    string
		wantFork      string
		wantSeedParts []string
	}{
		{
			name: "warm session resumes",
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				PriorEndedAt:   now.Add(-10 * time.Minute).Format(time.RFC3339),
			},
			wantVerdict: dshSessionStartResumed,
			wantResume:  priorID,
		},
		{
			name:  "cold session with a fresh summary is seeded",
			store: freshSummary,
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				PriorEndedAt:   priorEndedAt,
				Trigger:        "Now finish the tests.",
			},
			wantVerdict:   dshSessionStartSeededPrefix + priorID,
			wantFork:      priorID,
			wantSeedParts: []string{"parser", "Now finish the tests."},
		},
		{
			name:  "cold session with no summary starts fresh",
			store: summaryStore{},
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				PriorEndedAt:   priorEndedAt,
			},
			wantVerdict: dshSessionStartFresh,
		},
		{
			name:  "cold session with a stale summary starts fresh",
			store: staleSummary,
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				PriorEndedAt:   priorEndedAt,
			},
			wantVerdict: dshSessionStartFresh,
		},
		{
			name:        "no related session starts fresh",
			store:       summaryStore{},
			in:          dshSessionStartInput{},
			wantVerdict: dshSessionStartFresh,
		},
		{
			name:  "an unknown end time keeps today's resume",
			store: freshSummary,
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				// A server that predates prior_session_ended_at sends no time.
			},
			wantVerdict: dshSessionStartResumed,
			wantResume:  priorID,
		},
		{
			name:  "summaries disabled never seeds",
			store: freshSummary,
			in: dshSessionStartInput{
				PriorSessionID: priorID,
				PriorEndedAt:   priorEndedAt,
				Cfg:            DshSummaryConfig{Enabled: false, TTL: time.Hour, Margin: 15 * time.Minute},
			},
			wantVerdict: dshSessionStartFresh,
		},
		{
			name:  "a newer linked session wins over the same-issue one",
			store: summaryStore{},
			in: dshSessionStartInput{
				PriorSessionID:  priorID,
				PriorEndedAt:    now.Add(-3 * time.Hour).Format(time.RFC3339),
				LinkedSessionID: "sess-linked",
				LinkedIssueID:   "issue-linked",
				LinkedEndedAt:   now.Add(-5 * time.Minute).Format(time.RFC3339),
			},
			wantVerdict: dshSessionStartResumed,
			wantResume:  "sess-linked",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			in.Now = now
			if in.Cfg.TTL == 0 {
				in.Cfg = testSessionStartConfig()
			}
			in.ReadSummary = tc.store.read

			plan := decideDshSessionStart(in)

			if plan.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q (reason %q)", plan.Verdict, tc.wantVerdict, plan.Reason)
			}
			if plan.ResumeSessionID != tc.wantResume {
				t.Errorf("resume session = %q, want %q", plan.ResumeSessionID, tc.wantResume)
			}
			if plan.ForkParent != tc.wantFork {
				t.Errorf("fork parent = %q, want %q", plan.ForkParent, tc.wantFork)
			}
			if len(tc.wantSeedParts) == 0 && plan.Seed != "" {
				t.Errorf("seed = %q, want none", plan.Seed)
			}
			for _, part := range tc.wantSeedParts {
				if !strings.Contains(plan.Seed, part) {
					t.Errorf("seed %q does not carry %q", plan.Seed, part)
				}
			}
			if tc.wantFork != "" && plan.SeedTokens <= 0 {
				t.Errorf("a seeded plan must price its seed, got %d", plan.SeedTokens)
			}
		})
	}
}

// TestDecideDshSessionStartWarmBoundary pins the exact edge: TTL − margin is
// the last moment the cache counts as warm, and one second later it is cold.
func TestDecideDshSessionStartWarmBoundary(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	cfg := testSessionStartConfig()
	warmWindow := cfg.TTL - cfg.Margin

	for _, tc := range []struct {
		name    string
		idle    time.Duration
		want    string
		summary bool
	}{
		{name: "just inside the window", idle: warmWindow - time.Second, want: dshSessionStartResumed},
		{name: "exactly at the window", idle: warmWindow, want: dshSessionStartFresh, summary: true},
		{name: "past the window", idle: warmWindow + time.Hour, want: dshSessionStartFresh, summary: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ended := now.Add(-tc.idle)
			store := summaryStore{}
			if tc.summary {
				store["s"] = dshSessionSummaryRecord{
					Summary: "digest", MadeAt: ended.Add(time.Minute), ParentSession: "s",
				}
			}
			plan := decideDshSessionStart(dshSessionStartInput{
				Now:            now,
				Cfg:            cfg,
				PriorSessionID: "s",
				PriorEndedAt:   ended.Format(time.RFC3339),
				ReadSummary:    store.read,
			})
			if tc.want == dshSessionStartFresh {
				if plan.Verdict == dshSessionStartResumed {
					t.Fatalf("verdict = %q, want not-resumed", plan.Verdict)
				}
				return
			}
			if plan.Verdict != tc.want {
				t.Fatalf("verdict = %q, want %q", plan.Verdict, tc.want)
			}
		})
	}
}

// TestComposeDshSeedCapsAndTruncates covers the 32K ceiling: the trigger gives
// way first, and a summary that alone exceeds the cap keeps its head.
func TestComposeDshSeedCapsAndTruncates(t *testing.T) {
	t.Run("a huge trigger is truncated first", func(t *testing.T) {
		summary := strings.Repeat("s", 4000) // 1000 tokens
		trigger := strings.Repeat("t", 400000)
		seed, trimmed, dropped := composeDshSeed(summary, trigger)
		if !trimmed {
			t.Fatal("expected the seed to be trimmed")
		}
		if len(seed) > dshSeedTokenCap*4 {
			t.Fatalf("seed is %d bytes, cap is %d", len(seed), dshSeedTokenCap*4)
		}
		if !strings.Contains(seed, summary) {
			t.Error("the summary must survive a trigger-only overflow")
		}
		if !containsString(dropped, "trigger tail") {
			t.Errorf("dropped = %v, want the trigger tail named", dropped)
		}
		if containsString(dropped, "summary tail") {
			t.Errorf("dropped = %v, the summary fit", dropped)
		}
	})

	t.Run("a huge summary keeps its head and leaves the trigger a floor", func(t *testing.T) {
		summary := strings.Repeat("s", dshSeedTokenCap*4)
		trigger := "do the thing"
		seed, trimmed, dropped := composeDshSeed(summary, trigger)
		if !trimmed {
			t.Fatal("expected the seed to be trimmed")
		}
		if len(seed) > dshSeedTokenCap*4 {
			t.Fatalf("seed is %d bytes, cap is %d", len(seed), dshSeedTokenCap*4)
		}
		if !strings.Contains(seed, trigger) {
			t.Error("the trigger floor must survive a summary-only overflow")
		}
		if !containsString(dropped, "summary tail") {
			t.Errorf("dropped = %v, want the summary tail named", dropped)
		}
	})

	t.Run("a small seed is untouched", func(t *testing.T) {
		seed, trimmed, dropped := composeDshSeed("short digest", "short trigger")
		if trimmed || len(dropped) != 0 {
			t.Fatalf("trimmed=%v dropped=%v, want neither", trimmed, dropped)
		}
		if !strings.Contains(seed, "short digest") || !strings.Contains(seed, "short trigger") {
			t.Fatalf("seed = %q, want both parts", seed)
		}
	})

	t.Run("an empty summary composes nothing", func(t *testing.T) {
		if seed, _, _ := composeDshSeed("   ", "trigger"); seed != "" {
			t.Fatalf("seed = %q, want empty", seed)
		}
	})
}

// TestReadDshSummarySidecarMatchesWriter pins the reader to B1's writer: the
// path and format are a contract between two files, so a change to either
// must break this test rather than silently stop seeding.
func TestReadDshSummarySidecarMatchesWriter(t *testing.T) {
	root := t.TempDir()
	const cwd = "/mnt/workspace/work/m-138"
	const sessionID = "3f2b8f4e-1c4a-4f8e-9d59-0a1b2c3d4e5f"
	madeAt := time.Date(2026, 10, 30, 11, 45, 0, 0, time.UTC)

	record := dshSessionSummaryRecord{
		Summary:       "we finished the decision table",
		Tokens:        9,
		MadeAt:        madeAt,
		ParentSession: sessionID,
	}
	if err := writeDshSummarySidecar(root, cwd, sessionID, record); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	got, ok := readDshSummarySidecar(root, cwd, sessionID)
	if !ok {
		t.Fatal("reader did not find the sidecar the writer stored")
	}
	if got.Summary != record.Summary || !got.MadeAt.Equal(madeAt) || got.ParentSession != sessionID {
		t.Fatalf("round trip = %+v, want %+v", got, record)
	}

	if _, ok := readDshSummarySidecar(root, cwd, "no-such-session"); ok {
		t.Fatal("a missing sidecar must read as absent")
	}
	if _, ok := readDshSummarySidecar(root, "/somewhere/else", sessionID); ok {
		t.Fatal("a different cwd must not find this session's summary")
	}
	if _, ok := readDshSummarySidecar("", cwd, sessionID); ok {
		t.Fatal("an empty store root must read as absent")
	}

	// A sidecar that names a different session is mis-filed and unusable.
	misFiled := filepath.Join(root, dshsession.ProjectKey(cwd), sessionID)
	if err := os.WriteFile(filepath.Join(misFiled, dshSummarySidecarName), []byte(`{"summary":"x","parent_session":"other"}`), 0o600); err != nil {
		t.Fatalf("write mis-filed sidecar: %v", err)
	}
	if _, ok := readDshSummarySidecar(root, cwd, sessionID); ok {
		t.Fatal("a sidecar for another session must read as absent")
	}

	// Malformed JSON is not a summary either.
	if err := os.WriteFile(filepath.Join(misFiled, dshSummarySidecarName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed sidecar: %v", err)
	}
	if _, ok := readDshSummarySidecar(root, cwd, sessionID); ok {
		t.Fatal("malformed JSON must read as absent")
	}
}

// TestDshSessionStartRecordedOnTheRunRow pins the closed vocabulary: the run
// row's session_start is exactly one of the three strings, and a seeded
// verdict names the session it came from.
func TestDshSessionStartRecordedOnTheRunRow(t *testing.T) {
	verdicts := []string{
		dshSessionStartResumed,
		dshSessionStartSeededPrefix + "sess-1",
		dshSessionStartFresh,
	}
	if verdicts[0] != "resumed warm" || verdicts[2] != "fresh" {
		t.Fatalf("verdict strings changed: %q / %q", verdicts[0], verdicts[2])
	}
	if !strings.HasPrefix(verdicts[1], "seeded from summary of ") {
		t.Fatalf("seeded verdict = %q", verdicts[1])
	}
}

// TestDecideDshSessionStartUnreachableWarm pins F3: a warm candidate whose
// session store cannot be reached from this run never records "resumed"; it
// falls through to the seeded branch when a fresh summary exists, else fresh.
func TestDecideDshSessionStartUnreachableWarm(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	const priorID = "sess-prior"
	ended := now.Add(-10 * time.Minute)
	freshSummary := summaryStore{priorID: {
		Summary:       "digest",
		MadeAt:        ended.Add(time.Minute),
		ParentSession: priorID,
	}}

	for _, tc := range []struct {
		name    string
		store   summaryStore
		reach   bool
		want    string
	}{
		{name: "unreachable warm, fresh summary seeds", store: freshSummary, reach: false, want: dshSessionStartSeededPrefix + priorID},
		{name: "unreachable warm, no summary starts fresh", store: summaryStore{}, reach: false, want: dshSessionStartFresh},
		{name: "reachable warm resumes", store: summaryStore{}, reach: true, want: dshSessionStartResumed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := dshSessionStartInput{
				Now:            now,
				Cfg:            testSessionStartConfig(),
				PriorSessionID: priorID,
				PriorEndedAt:   ended.Format(time.RFC3339),
				PriorWorkDir:   "/wd",
				ReadSummary:    tc.store.read,
				Reachable:      func(dshSessionCandidate) bool { return tc.reach },
			}
			if tc.store == nil {
				in.ReadSummary = summaryStore{}.read
			}
			plan := decideDshSessionStart(in)
			if plan.Verdict != tc.want {
				t.Fatalf("verdict = %q, want %q (reason %q)", plan.Verdict, tc.want, plan.Reason)
			}
			if tc.want == dshSessionStartResumed && plan.ResumeSessionID != priorID {
				t.Fatalf("resume = %q, want %q", plan.ResumeSessionID, priorID)
			}
		})
	}
}

// TestDecideDshSessionStartLinkedWarm pins the linked-session semantics: a
// warm linked session is only resumed when its store is reachable from this
// run; otherwise it seeds from its summary (or starts fresh).
func TestDecideDshSessionStartLinkedWarm(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	const linkedID = "sess-linked"
	ended := now.Add(-5 * time.Minute)
	freshSummary := summaryStore{linkedID: {
		Summary:       "linked digest",
		MadeAt:        ended.Add(time.Minute),
		ParentSession: linkedID,
	}}

	t.Run("reachable linked warm resumes", func(t *testing.T) {
		in := dshSessionStartInput{
			Now:             now,
			Cfg:             testSessionStartConfig(),
			LinkedSessionID: linkedID,
			LinkedEndedAt:   ended.Format(time.RFC3339),
			LinkedWorkDir:   "/wd",
			ReadSummary:     freshSummary.read,
			Reachable:       func(dshSessionCandidate) bool { return true },
		}
		plan := decideDshSessionStart(in)
		if plan.Verdict != dshSessionStartResumed || plan.ResumeSessionID != linkedID {
			t.Fatalf("verdict=%q resume=%q", plan.Verdict, plan.ResumeSessionID)
		}
	})

	t.Run("unreachable linked warm seeds from summary", func(t *testing.T) {
		in := dshSessionStartInput{
			Now:             now,
			Cfg:             testSessionStartConfig(),
			LinkedSessionID: linkedID,
			LinkedEndedAt:   ended.Format(time.RFC3339),
			LinkedWorkDir:   "/other",
			ReadSummary:     freshSummary.read,
			Reachable:       func(dshSessionCandidate) bool { return false },
		}
		plan := decideDshSessionStart(in)
		if plan.Verdict != dshSessionStartSeededPrefix+linkedID {
			t.Fatalf("verdict=%q", plan.Verdict)
		}
	})
}
