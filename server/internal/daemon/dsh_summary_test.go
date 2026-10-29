package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/dshsession"
)

// fakeSummaryScheduler records one scheduled fire and lets the test decide
// when (and whether) it runs — the fake clock the acceptance checks drive.
type fakeSummaryScheduler struct {
	mu    sync.Mutex
	fires []fakeSummaryFire
}

type fakeSummaryFire struct {
	Delay     time.Duration
	Cancelled bool
	Fire      func()
}

func (s *fakeSummaryScheduler) Schedule(delay time.Duration, fire func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := len(s.fires)
	s.fires = append(s.fires, fakeSummaryFire{Delay: delay, Fire: fire})
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.fires[index].Cancelled = true
	}
}

func (s *fakeSummaryScheduler) fire(t *testing.T, index int) {
	t.Helper()
	s.mu.Lock()
	entry := s.fires[index]
	s.mu.Unlock()
	if entry.Cancelled {
		t.Fatalf("fire %d was cancelled", index)
	}
	entry.Fire()
}

func (s *fakeSummaryScheduler) delay(t *testing.T, index int) time.Duration {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fires[index].Delay
}

// testSummarySettings is the acceptance configuration: a 120 s TTL with a
// 30 s margin — the summary must be built at TTL−margin = 90 s.
func testSummarySettings() DshSummaryConfig {
	return DshSummaryConfig{
		Enabled:         true,
		SummaryTokens:   2000,
		SummaryTokenCap: 4096,
		TriggerTokens:   1000,
		TriggerIdle:     30 * time.Minute,
		TTL:             120 * time.Second,
		Margin:          30 * time.Second,
		ColdWarmRatio:   30,
	}
}

// newTestSummaryClock wires a clock onto the fake scheduler and a frozen
// clock; now starts at epoch and the test advances it explicitly.
func newTestSummaryClock(t *testing.T, cfg DshSummaryConfig) (*dshSummaryClock, *fakeSummaryScheduler, *time.Time) {
	t.Helper()
	scheduler := &fakeSummaryScheduler{}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clock := newDshSummaryClock(cfg, slog.Default())
	clock.now = func() time.Time { return now }
	clock.scheduler = scheduler
	return clock, scheduler, &now
}

// TestDshSummaryClockBuildsAtTTLMargin is the acceptance check: with a 120 s
// TTL and a conversation above the trigger, the summary is built at
// TTL−margin, is at most the 2K target, and lands beside the session with
// parent_session set.
func TestDshSummaryClockBuildsAtTTLMargin(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	cfg := testSummarySettings()
	clock, scheduler, now := newTestSummaryClock(t, cfg)

	var records []dshSummaryRunRowRecord
	var gotSummary *agent.DshSessionSummary
	// ~1.5K tokens by the bytes/4 estimate — under the 2K target.
	summaryText := strings.Repeat("summary token ", 1500)

	// Two run ends: the first establishes the init-prefix baseline, the
	// second grows the conversation well above the trigger.
	arm := func(total int64) {
		clock.Arm(dshSummaryRequest{
			TaskID:            "task-1",
			SessionID:         "session-1",
			WorkDir:           workDir,
			StoreRoot:         storeRoot,
			Model:             "deepseek/deepseek-v4-pro",
			TotalPromptTokens: total,
			Summarize: func(_ context.Context, sessionID, model string) (*agent.DshSessionSummary, error) {
				gotSummary = &agent.DshSessionSummary{SessionID: sessionID, Summary: summaryText, Tokens: 1500, Model: model}
				return gotSummary, nil
			},
			RecordDecision: func(_ context.Context, record dshSummaryRunRowRecord) {
				records = append(records, record)
			},
		})
	}
	arm(8000)
	*now = now.Add(90 * time.Second)
	arm(58000)

	if got := scheduler.delay(t, 1); got != 90*time.Second {
		t.Fatalf("clock armed for %s, want TTL−margin = 90s", got)
	}

	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 1)

	if gotSummary == nil {
		t.Fatal("summarize was not invoked at TTL−margin")
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonProceed {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if records[0].TokensAboveInit != 50000 {
		t.Fatalf("tokens above init = %d, want 50000", records[0].TokensAboveInit)
	}

	record := readSummarySidecar(t, storeRoot, workDir, "session-1")
	if record.ParentSession != "session-1" {
		t.Fatalf("parent_session = %q, want session-1", record.ParentSession)
	}
	if record.Tokens > 2000 {
		t.Fatalf("summary is %d tokens, want ≤ 2K", record.Tokens)
	}
	if record.Summary != summaryText {
		t.Fatalf("stored summary = %q", record.Summary)
	}
	if !record.MadeAt.Equal(*now) {
		t.Fatalf("made_at = %s, want the fire time %s", record.MadeAt, *now)
	}
}

// TestDshSummaryClockSkipsBelowTrigger: nothing is built when the
// conversation stayed under the trigger and the idle time under the idle
// trigger — the decision is still recorded, with its token counts.
func TestDshSummaryClockSkipsBelowTrigger(t *testing.T) {
	storeRoot := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())

	summarized := false
	var records []dshSummaryRunRowRecord
	clock.Arm(dshSummaryRequest{
		TaskID:            "task-2",
		SessionID:         "session-2",
		WorkDir:           t.TempDir(),
		StoreRoot:         storeRoot,
		Model:             "deepseek/deepseek-v4-pro",
		TotalPromptTokens: 4000,
		Summarize: func(context.Context, string, string) (*agent.DshSessionSummary, error) {
			summarized = true
			return nil, errors.New("must not be called")
		},
		RecordDecision: func(_ context.Context, record dshSummaryRunRowRecord) {
			records = append(records, record)
		},
	})
	// First arm: baseline = 4000, so growth is 0 at the 90 s fire.
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if summarized {
		t.Fatal("summarize was invoked below the trigger")
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonBelow {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if records[0].TokensAboveInit != 0 {
		t.Fatalf("tokens above init = %d, want 0", records[0].TokensAboveInit)
	}
	found := false
	_ = filepath.Walk(storeRoot, func(path string, _ os.FileInfo, _ error) error {
		if filepath.Base(path) == dshSummarySidecarName {
			found = true
		}
		return nil
	})
	if found {
		t.Fatal("a sidecar was written below the trigger")
	}
}

// TestDshSummaryClockCostRule: a conversation above the trigger but too small
// to justify a summary — the expected cold re-read costs less than building
// the summary — is skipped with the cost reason.
func TestDshSummaryClockCostRule(t *testing.T) {
	cfg := testSummarySettings()
	cfg.TriggerTokens = 10 // trigger reached...
	clock, scheduler, now := newTestSummaryClock(t, cfg)

	summarized := false
	var records []dshSummaryRunRowRecord
	record := func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) }
	summarize := func(context.Context, string, string) (*agent.DshSessionSummary, error) {
		summarized = true
		return &agent.DshSessionSummary{Summary: "x", Tokens: 1}, nil
	}
	// ...but 50 tokens × 29 price gap = 1450 < 2000 summary production cost.
	arm := func(session string, total int64) {
		clock.Arm(dshSummaryRequest{SessionID: session, TotalPromptTokens: total, Summarize: summarize, RecordDecision: record, StoreRoot: t.TempDir()})
	}
	arm("s-small-a", 100)
	arm("s-small-b", 100)
	*now = now.Add(90 * time.Second)
	arm("s-small-a", 150)
	arm("s-small-b", 150)

	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 2)
	scheduler.fire(t, 3)

	if summarized {
		t.Fatal("summarize ran even though the cost rule said no")
	}
	for _, r := range records {
		if r.Decision != dshSummaryReasonCost {
			t.Fatalf("decision = %q, want %q", r.Decision, dshSummaryReasonCost)
		}
	}
}

// TestDshSummaryClockCancelOnResume: a resumed session disarms its clock —
// the pending fire never runs and nothing is stored.
func TestDshSummaryClockCancelOnResume(t *testing.T) {
	clock, scheduler, _ := newTestSummaryClock(t, testSummarySettings())

	summarized := false
	clock.Arm(dshSummaryRequest{
		SessionID:         "session-3",
		TotalPromptTokens: 90000,
		Summarize: func(context.Context, string, string) (*agent.DshSessionSummary, error) {
			summarized = true
			return &agent.DshSessionSummary{Summary: "x", Tokens: 1}, nil
		},
		StoreRoot: t.TempDir(),
	})
	clock.Cancel("session-3")
	if !scheduler.fires[0].Cancelled {
		t.Fatal("pending fire was not cancelled on resume")
	}
	if summarized {
		t.Fatal("summarize ran for a resumed session")
	}
}

// TestDshSummaryClockUnsupportedBridge: a bridge without the summarize
// capability is a recorded skip, not a failure.
func TestDshSummaryClockUnsupportedBridge(t *testing.T) {
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	var records []dshSummaryRunRowRecord
	arm := func(total int64) {
		clock.Arm(dshSummaryRequest{
			SessionID:         "session-4",
			TotalPromptTokens: total,
			Summarize: func(context.Context, string, string) (*agent.DshSessionSummary, error) {
				return nil, agent.ErrDshSummarizeUnsupported
			},
			RecordDecision: func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) },
			StoreRoot:      t.TempDir(),
		})
	}
	arm(8000)
	*now = now.Add(90 * time.Second)
	arm(90000)
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 1)
	if len(records) != 1 || records[0].Decision != dshSummaryReasonNoBridge {
		t.Fatalf("unexpected decisions: %#v", records)
	}
}

// TestDecideDshSummary pins the trigger and cost clauses, including the
// default-config behavior where idle at the 45 min fire satisfies the idle
// trigger on its own.
func TestDecideDshSummary(t *testing.T) {
	cfg := DshSummaryConfig{
		SummaryTokens: 2000, TriggerTokens: 20000, TriggerIdle: 30 * time.Minute,
		TTL: time.Hour, Margin: 15 * time.Minute, ColdWarmRatio: 30,
	}
	cases := []struct {
		name    string
		tokens  int64
		idle    time.Duration
		proceed bool
		reason  string
	}{
		{"idle trigger fires a real conversation", 100, 45 * time.Minute, true, dshSummaryReasonProceed},
		{"big conversation at fire", 50000, 45 * time.Minute, true, dshSummaryReasonProceed},
		{"idle but nothing above the init prefix", 0, 45 * time.Minute, false, dshSummaryReasonCost},
		{"tiny idle conversation", 100, time.Minute, false, dshSummaryReasonBelow},
		{"below idle, mid-size tokens", 15000, time.Minute, false, dshSummaryReasonBelow},
		{"idle trigger met, cost not justified", 50, 40 * time.Minute, false, dshSummaryReasonCost},
		{"trigger met by tokens with short idle", 50000, time.Second, true, dshSummaryReasonProceed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDshSummary(tc.tokens, tc.idle, cfg)
			if got.Proceed != tc.proceed || got.Reason != tc.reason {
				t.Fatalf("decide(%d tokens, %s) = %#v, want proceed=%v reason=%q",
					tc.tokens, tc.idle, got, tc.proceed, tc.reason)
			}
		})
	}
}

// TestDshSummaryConfigTTLForModel: the per-model promptCache wins over the
// daemon-wide TTL, long before short.
func TestDshSummaryConfigTTLForModel(t *testing.T) {
	cfg := DshSummaryConfig{
		TTL: time.Hour,
		PromptCache: map[string]DshPromptCacheConfig{
			"deepseek/deepseek-v4-flash": {Short: 5 * time.Minute, Long: 12 * time.Hour},
			"deepseek/tiny":              {Short: 2 * time.Minute},
		},
	}
	if got := cfg.ttlForModel("deepseek/deepseek-v4-flash"); got != 12*time.Hour {
		t.Fatalf("flash TTL = %s, want its long TTL 12h", got)
	}
	if got := cfg.ttlForModel("deepseek/tiny"); got != 2*time.Minute {
		t.Fatalf("tiny TTL = %s, want its short TTL 2m", got)
	}
	if got := cfg.ttlForModel("deepseek/unknown"); got != time.Hour {
		t.Fatalf("unknown model TTL = %s, want the daemon default 1h", got)
	}
}

// TestDshSummaryConfigFromEnv: defaults come from the M-133 spike; every
// value is overridable; a malformed value is an error, not a silent default.
func TestDshSummaryConfigFromEnv(t *testing.T) {
	for _, key := range os.Environ() {
		if strings.HasPrefix(key, "MULTICA_DSH_") {
			name, _, _ := strings.Cut(key, "=")
			t.Setenv(name, "")
		}
	}

	cfg, err := dshSummaryConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.SummaryTokens != DefaultDshSummaryTokens ||
		cfg.SummaryTokenCap != DefaultDshSummaryTokenCap ||
		cfg.TriggerTokens != DefaultDshSummaryTriggerTokens ||
		cfg.TriggerIdle != DefaultDshSummaryTriggerIdle ||
		cfg.TTL != DefaultDshSummaryTTL || cfg.Margin != DefaultDshSummaryMargin ||
		cfg.ColdWarmRatio != DefaultDshSummaryColdWarmRatio {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}

	t.Setenv("MULTICA_DSH_SUMMARY_TTL", "120s")
	t.Setenv("MULTICA_DSH_SUMMARY_MARGIN", "30s")
	t.Setenv("MULTICA_DSH_SUMMARY_TOKENS", "1024")
	t.Setenv("MULTICA_DSH_SUMMARY_ENABLED", "0")
	t.Setenv("MULTICA_DSH_PROMPT_CACHE", `{"deepseek/v4-pro": {"short": "5m", "long": "1h"}}`)
	cfg, err = dshSummaryConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.TTL != 120*time.Second || cfg.Margin != 30*time.Second || cfg.SummaryTokens != 1024 {
		t.Fatalf("overrides not applied: %#v", cfg)
	}
	if pc := cfg.PromptCache["deepseek/v4-pro"]; pc.Short != 5*time.Minute || pc.Long != time.Hour {
		t.Fatalf("prompt cache not applied: %#v", pc)
	}

	t.Setenv("MULTICA_DSH_PROMPT_CACHE", `{"broken": {"short": "soon"}}`)
	if _, err := dshSummaryConfigFromEnv(); err == nil {
		t.Fatal("a malformed prompt cache must be an error, not a fallback")
	}
}

// TestWriteDshSummarySidecar: the record lands beside the session in the dsh
// store layout and round-trips all four fields.
func TestWriteDshSummarySidecar(t *testing.T) {
	storeRoot := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "work")
	madeAt := time.Date(2026, 10, 29, 12, 45, 0, 0, time.UTC)
	record := dshSessionSummaryRecord{
		Summary: "The session implemented X.", Tokens: 600, MadeAt: madeAt, ParentSession: "session-1",
	}
	if err := writeDshSummarySidecar(storeRoot, cwd, "session-1", record); err != nil {
		t.Fatal(err)
	}
	stored := readSummarySidecar(t, storeRoot, cwd, "session-1")
	if stored != record {
		t.Fatalf("round trip = %#v, want %#v", stored, record)
	}

	if err := writeDshSummarySidecar(storeRoot, cwd, "session/../evil", record); err == nil {
		t.Fatal("a traversal session id must be rejected")
	}
	if err := writeDshSummarySidecar("", cwd, "session-1", record); err == nil {
		t.Fatal("an empty store root must be rejected")
	}
}

func readSummarySidecar(t *testing.T, storeRoot, cwd, sessionID string) dshSessionSummaryRecord {
	t.Helper()
	path := filepath.Join(storeRoot, dshsession.ProjectKey(cwd), sessionID, dshSummarySidecarName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar %s: %v", path, err)
	}
	var record dshSessionSummaryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	return record
}
