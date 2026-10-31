package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		TTL:             120 * time.Second,
		Margin:          30 * time.Second,
		ColdWarmRatio:   30,
	}
}

func newTestSummaryClock(t *testing.T, cfg DshSummaryConfig) (*dshSummaryClock, *fakeSummaryScheduler, *time.Time) {
	t.Helper()
	scheduler := &fakeSummaryScheduler{}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clock := newDshSummaryClock(cfg, slog.Default())
	clock.now = func() time.Time { return now }
	clock.scheduler = scheduler
	return clock, scheduler, &now
}

// armOneShot is the one-shot acceptance shape: a single run that started its
// session, whose first model request was the init prefix and whose last
// request grew the conversation above it. No resume, no second arm — the
// run itself must be able to reach the summarize decision.
func armOneShot(clock *dshSummaryClock, storeRoot, workDir string, first, last int64, summarize func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error), record func(context.Context, dshSummaryRunRowRecord)) {
	clock.Arm(dshSummaryRequest{
		TaskID:            "task-1",
		SessionID:         "session-1",
		WorkDir:           workDir,
		StoreRoot:         storeRoot,
		Model:             "deepseek/deepseek-v4-pro",
		FirstPromptTokens: first,
		LastPromptTokens:  last,
		Summarize:         summarize,
		RecordDecision:    record,
	})
}

// TestDshSummaryClockBuildsOneShotAtTTLMargin is the acceptance check
// (Architect review, must-fix 1 + 4): with a 120 s TTL, a one-shot session —
// first request 20K (init prefix), last request 60K, no earlier arm — builds
// its summary at TTL−margin = 90 s, the summary is at most the 2K target,
// and it lands beside the session with parent_session set.
func TestDshSummaryClockBuildsOneShotAtTTLMargin(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	cfg := testSummarySettings()
	clock, scheduler, now := newTestSummaryClock(t, cfg)

	var records []dshSummaryRunRowRecord
	var gotSummary *agent.DshSessionSummary
	// ~1.5K tokens by the bytes/4 estimate — under the 2K target.
	summaryText := strings.Repeat("summary token ", 1500)

	armOneShot(clock, storeRoot, workDir, 20000, 60000,
		func(_ context.Context, sessionID, model, _ string, _ int64) (*agent.DshSessionSummary, error) {
			gotSummary = &agent.DshSessionSummary{SessionID: sessionID, Summary: summaryText, Tokens: 1500, Model: model}
			return gotSummary, nil
		},
		func(_ context.Context, record dshSummaryRunRowRecord) {
			records = append(records, record)
		})

	if got := scheduler.delay(t, 0); got != 90*time.Second {
		t.Fatalf("clock armed for %s, want TTL−margin = 90s", got)
	}

	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if gotSummary == nil {
		t.Fatal("summarize was not invoked at TTL−margin for a one-shot session")
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonProceed {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if records[0].TokensAboveInit != 40000 {
		t.Fatalf("tokens above init = %d, want 40000 (60K last − 20K first)",
			records[0].TokensAboveInit)
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

// TestDshSummaryClockSkipsBelowTrigger: a tiny session — the conversation
// never reached the trigger above its init prefix — is skipped with the
// below-trigger reason when the clock fires; nothing is built.
func TestDshSummaryClockSkipsBelowTrigger(t *testing.T) {
	storeRoot := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())

	summarized := false
	var records []dshSummaryRunRowRecord
	// First request 4K (init prefix), last request 4.5K: growth of 500
	// tokens stays under the 1K trigger.
	armOneShot(clock, storeRoot, t.TempDir(), 4000, 4500,
		func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
			summarized = true
			return nil, errors.New("must not be called")
		},
		func(_ context.Context, record dshSummaryRunRowRecord) {
			records = append(records, record)
		})
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if summarized {
		t.Fatal("summarize was invoked below the trigger")
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonBelow {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if records[0].TokensAboveInit != 500 {
		t.Fatalf("tokens above init = %d, want 500", records[0].TokensAboveInit)
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

// TestDshSummaryClockCostRule: a conversation above the trigger but too
// small to justify a summary — the expected cold re-read costs less than
// building the summary — is skipped with the cost reason.
func TestDshSummaryClockCostRule(t *testing.T) {
	cfg := testSummarySettings()
	cfg.TriggerTokens = 10 // trigger reached...
	clock, scheduler, now := newTestSummaryClock(t, cfg)

	summarized := false
	var records []dshSummaryRunRowRecord
	// ...but growth of 50 tokens × 29 price gap = 1450 < 2000 summary
	// production cost, so the cost rule says no.
	armOneShot(clock, t.TempDir(), t.TempDir(), 100, 150,
		func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
			summarized = true
			return &agent.DshSessionSummary{Summary: "x", Tokens: 1}, nil
		},
		func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) })

	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if summarized {
		t.Fatal("summarize ran even though the cost rule said no")
	}
	if len(records) != 1 || records[0].Decision != dshSummaryReasonCost {
		t.Fatalf("unexpected decisions: %#v", records)
	}
}

// TestDshSummaryClockReArmsWithNewFacts: a second run for the same session
// replaces the pending fire and its facts with the newer run's — the newer
// last-request size is what the fire decision uses.
func TestDshSummaryClockReArmsWithNewFacts(t *testing.T) {
	storeRoot := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())

	var records []dshSummaryRunRowRecord
	summarize := func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
		return &agent.DshSessionSummary{Summary: "x", Tokens: 1}, nil
	}
	record := func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) }

	armOneShot(clock, storeRoot, t.TempDir(), 20000, 25000, summarize, record)
	*now = now.Add(90 * time.Second)
	// The same session is run again before the first fire: its last request
	// grew the conversation to 60K, and that newer fact replaces the first.
	armOneShot(clock, storeRoot, t.TempDir(), 20000, 60000, summarize, record)

	if !scheduler.fires[0].Cancelled {
		t.Fatal("the first pending fire was not replaced by the re-arm")
	}
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 1)
	if len(records) != 1 || records[0].Decision != dshSummaryReasonProceed {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if records[0].TokensAboveInit != 40000 {
		t.Fatalf("tokens above init = %d, want the newer run's 40000", records[0].TokensAboveInit)
	}
}

// TestDshSummaryClockCancelOnResume: a resumed session disarms its clock —
// the pending fire never runs and nothing is stored.
func TestDshSummaryClockCancelOnResume(t *testing.T) {
	clock, scheduler, _ := newTestSummaryClock(t, testSummarySettings())

	summarized := false
	armOneShot(clock, t.TempDir(), t.TempDir(), 20000, 60000,
		func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
			summarized = true
			return &agent.DshSessionSummary{Summary: "x", Tokens: 1}, nil
		}, nil)
	clock.Cancel("session-1")
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
	armOneShot(clock, t.TempDir(), t.TempDir(), 20000, 60000,
		func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
			return nil, agent.ErrDshSummarizeUnsupported
		},
		func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) })
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)
	if len(records) != 1 || records[0].Decision != dshSummaryReasonNoBridge {
		t.Fatalf("unexpected decisions: %#v", records)
	}
}

// TestDecideDshSummary pins the token trigger and cost clauses. The decision
// is token-only (Architect review, must-fix 2): the clock fires at TTL−margin
// by construction, so idle time is not a trigger input.
func TestDecideDshSummary(t *testing.T) {
	cfg := DshSummaryConfig{
		SummaryTokens: 2000, TriggerTokens: 20000,
		TTL: time.Hour, Margin: 15 * time.Minute, ColdWarmRatio: 30,
	}
	cases := []struct {
		name    string
		tokens  int64
		proceed bool
		reason  string
	}{
		{"big conversation at fire", 50000, true, dshSummaryReasonProceed},
		{"nothing above the init prefix", 0, false, dshSummaryReasonBelow},
		{"just under the trigger", 19999, false, dshSummaryReasonBelow},
		{"at the trigger, cost justified", 20000, true, dshSummaryReasonProceed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDshSummary(tc.tokens, cfg)
			if got.Proceed != tc.proceed || got.Reason != tc.reason {
				t.Fatalf("decide(%d tokens) = %#v, want proceed=%v reason=%q",
					tc.tokens, got, tc.proceed, tc.reason)
			}
		})
	}
	// Above the trigger but the cache has almost no price advantage: the cost
	// rule skips what the trigger alone would build.
	cheap := cfg
	cheap.ColdWarmRatio = 1.05 // gap = 20100 × 0.05 = 1005 < 2000 summary cost
	if got := decideDshSummary(20100, cheap); got.Proceed || got.Reason != dshSummaryReasonCost {
		t.Fatalf("decide(20100, cheap cache) = %#v, want skipped-cost", got)
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
	if _, err = dshSummaryConfigFromEnv(); err == nil {
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

// armDshRun arms the clock for one run of a session and returns the decision
// the fired clock recorded.
func armDshRun(t *testing.T, clock *dshSummaryClock, scheduler *fakeSummaryScheduler, now *time.Time, req dshSummaryRequest) dshSummaryRunRowRecord {
	t.Helper()
	var records []dshSummaryRunRowRecord
	req.TaskID = "task-1"
	req.Model = "deepseek/deepseek-v4-pro"
	req.Summarize = func(_ context.Context, sessionID, model, _ string, _ int64) (*agent.DshSessionSummary, error) {
		return &agent.DshSessionSummary{SessionID: sessionID, Summary: "summary", Tokens: 100, Model: model}, nil
	}
	req.RecordDecision = func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) }
	fireIndex := len(scheduler.fires)
	clock.Arm(req)
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, fireIndex)
	if len(records) != 1 {
		t.Fatalf("expected exactly one decision, got %#v", records)
	}
	return records[0]
}

func readInitPrefixFile(t *testing.T, storeRoot, workDir, sessionID string) (dshSessionInitPrefixRecord, bool) {
	t.Helper()
	path := filepath.Join(storeRoot, dshsession.ProjectKey(workDir), sessionID, dshSessionInitPrefixSidecarName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return dshSessionInitPrefixRecord{}, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var record dshSessionInitPrefixRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return record, true
}

// TestDshSummaryClockResumedRunMeasuresGrowthAboveFirstRunInitPrefix: the init
// prefix belongs to the session's first run. A resumed run's own first
// request already carries the whole history, so growth must be measured
// against the persisted prefix, not the resumed run's FirstPromptTokens.
func TestDshSummaryClockResumedRunMeasuresGrowthAboveFirstRunInitPrefix(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	cfg := testSummarySettings()
	cfg.TriggerTokens = 20000
	clock, scheduler, now := newTestSummaryClock(t, cfg)

	// Run 1: fresh session. Growth 18000-10000 = 8000 stays under the trigger.
	first := armDshRun(t, clock, scheduler, now, dshSummaryRequest{
		SessionID: "session-1", WorkDir: workDir, StoreRoot: storeRoot,
		FirstPromptTokens: 10000, LastPromptTokens: 18000,
	})
	if first.Decision != dshSummaryReasonBelow || first.TokensAboveInit != 8000 {
		t.Fatalf("run 1 decision = %#v, want below-trigger with 8000 tokens above init", first)
	}
	prefix, ok := readInitPrefixFile(t, storeRoot, workDir, "session-1")
	if !ok || prefix.Tokens != 10000 {
		t.Fatalf("init_prefix.json after run 1 = %#v (exists=%v), want 10000", prefix, ok)
	}

	// Run 2: resumed. Measured against its own first request the growth would
	// be 32000-18000 = 14000 (skip); against the persisted prefix it is 22000.
	second := armDshRun(t, clock, scheduler, now, dshSummaryRequest{
		SessionID: "session-1", WorkDir: workDir, StoreRoot: storeRoot,
		PriorSessionID:    "session-1",
		FirstPromptTokens: 18000, LastPromptTokens: 32000,
	})
	if second.Decision != dshSummaryReasonProceed {
		t.Fatalf("run 2 decision = %q, want %q", second.Decision, dshSummaryReasonProceed)
	}
	if second.TokensAboveInit != 22000 {
		t.Fatalf("run 2 tokens above init = %d, want 22000 (32000 − persisted 10000)", second.TokensAboveInit)
	}
	if prefix, _ := readInitPrefixFile(t, storeRoot, workDir, "session-1"); prefix.Tokens != 10000 {
		t.Fatalf("resumed run overwrote the init prefix: %d, want 10000", prefix.Tokens)
	}
}

// TestDshSummaryClockResumedRunDoesNotCreateInitPrefix: only a session's first
// run may record the init prefix; a resumed run of a session with no record
// (older session) must not invent one from its history-laden first request.
func TestDshSummaryClockResumedRunDoesNotCreateInitPrefix(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())

	armDshRun(t, clock, scheduler, now, dshSummaryRequest{
		SessionID: "session-1", WorkDir: workDir, StoreRoot: storeRoot,
		PriorSessionID:    "session-1",
		FirstPromptTokens: 18000, LastPromptTokens: 32000,
	})
	if _, ok := readInitPrefixFile(t, storeRoot, workDir, "session-1"); ok {
		t.Fatal("a resumed run created init_prefix.json")
	}
}

// TestDshSummaryConfigFromEnvValidation: unusable timing or enabled settings
// fail loudly at load time instead of silently disarming the clock.
func TestDshSummaryConfigFromEnvValidation(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string // substring; empty means no error expected
	}{
		{"negative margin", map[string]string{"MULTICA_DSH_SUMMARY_MARGIN": "-1m"}, "MULTICA_DSH_SUMMARY_MARGIN"},
		{"margin equals ttl", map[string]string{"MULTICA_DSH_SUMMARY_TTL": "1h", "MULTICA_DSH_SUMMARY_MARGIN": "1h"}, "shorter than the TTL"},
		{"margin exceeds ttl", map[string]string{"MULTICA_DSH_SUMMARY_TTL": "1h", "MULTICA_DSH_SUMMARY_MARGIN": "2h"}, "shorter than the TTL"},
		{
			"margin not shorter than a model ttl",
			map[string]string{
				"MULTICA_DSH_SUMMARY_TTL":    "1h",
				"MULTICA_DSH_SUMMARY_MARGIN": "15m",
				"MULTICA_DSH_PROMPT_CACHE":   `{"deepseek/short-model": {"short": "10m"}}`,
			},
			"deepseek/short-model",
		},
		{
			"valid per-model ttl",
			map[string]string{
				"MULTICA_DSH_SUMMARY_TTL":    "1h",
				"MULTICA_DSH_SUMMARY_MARGIN": "15m",
				"MULTICA_DSH_PROMPT_CACHE":   `{"deepseek/long-model": {"short": "30m", "long": "12h"}}`,
			},
			"",
		},
		{"enabled maybe", map[string]string{"MULTICA_DSH_SUMMARY_ENABLED": "maybe"}, "MULTICA_DSH_SUMMARY_ENABLED"},
		{"enabled off", map[string]string{"MULTICA_DSH_SUMMARY_ENABLED": "off"}, ""},
		{"enabled 0", map[string]string{"MULTICA_DSH_SUMMARY_ENABLED": "0"}, ""},
		{"enabled true", map[string]string{"MULTICA_DSH_SUMMARY_ENABLED": "true"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range os.Environ() {
				if strings.HasPrefix(key, "MULTICA_DSH_") {
					name, _, _ := strings.Cut(key, "=")
					t.Setenv(name, "")
				}
			}
			for name, value := range tc.env {
				t.Setenv(name, value)
			}
			_, err := dshSummaryConfigFromEnv()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func summarySidecarExists(t *testing.T, storeRoot, workDir, sessionID string) bool {
	t.Helper()
	path := filepath.Join(storeRoot, dshsession.ProjectKey(workDir), sessionID, dshSummarySidecarName)
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat sidecar: %v", err)
	}
	return false
}

// TestDshSummaryClockRejectsOverCapSummary is the F5 regression: a summary
// whose reported size exceeds the cap is recorded as summary-over-cap and is
// never written, so the next session start falls to fresh.
func TestDshSummaryClockRejectsOverCapSummary(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	var records []dshSummaryRunRowRecord
	armOneShot(clock, storeRoot, workDir, 20000, 60000,
		func(_ context.Context, sessionID, model, _ string, _ int64) (*agent.DshSessionSummary, error) {
			return &agent.DshSessionSummary{SessionID: sessionID, Summary: "too long", Tokens: 5000, Model: model}, nil
		},
		func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) })
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if len(records) != 1 || records[0].Decision != dshSummaryReasonOverCap || records[0].SummaryTokens != 5000 {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if summarySidecarExists(t, storeRoot, workDir, "session-1") {
		t.Fatal("over-cap summary must not be stored")
	}
}

// TestDshSummaryClockOverCapFromBridge: a bridge that enforces the budget
// itself answers DSH_SUMMARY_OVER_CAP; that is the over-cap decision, not a
// generic summarize failure.
func TestDshSummaryClockOverCapFromBridge(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	var records []dshSummaryRunRowRecord
	armOneShot(clock, storeRoot, workDir, 20000, 60000,
		func(context.Context, string, string, string, int64) (*agent.DshSessionSummary, error) {
			return nil, fmt.Errorf("dsh summarize failed: DSH_SUMMARY_OVER_CAP: %w", agent.ErrDshSummaryOverCap)
		},
		func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) })
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if len(records) != 1 || records[0].Decision != dshSummaryReasonOverCap {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if summarySidecarExists(t, storeRoot, workDir, "session-1") {
		t.Fatal("no sidecar may exist after an over-cap rejection")
	}
}

// TestDshSummaryClockPassesWorkDirAndCap: the bridge must run in the same
// directory the sidecar is keyed by (F15) and receive the configured cap.
func TestDshSummaryClockPassesWorkDirAndCap(t *testing.T) {
	workDir := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	var gotWorkDir string
	var gotCap int64
	armOneShot(clock, t.TempDir(), workDir, 20000, 60000,
		func(_ context.Context, sessionID, model, cwd string, maxTokens int64) (*agent.DshSessionSummary, error) {
			gotWorkDir, gotCap = cwd, maxTokens
			return &agent.DshSessionSummary{SessionID: sessionID, Summary: "ok", Tokens: 10, Model: model}, nil
		},
		func(context.Context, dshSummaryRunRowRecord) {})
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if gotWorkDir != workDir || gotCap != 4096 {
		t.Fatalf("Summarize got workDir %q cap %d, want %q and 4096", gotWorkDir, gotCap, workDir)
	}
}

// TestDshSummaryClockStoresSummaryAtCap pins the boundary: exactly the cap is
// still stored.
func TestDshSummaryClockStoresSummaryAtCap(t *testing.T) {
	storeRoot := t.TempDir()
	workDir := t.TempDir()
	clock, scheduler, now := newTestSummaryClock(t, testSummarySettings())
	var records []dshSummaryRunRowRecord
	armOneShot(clock, storeRoot, workDir, 20000, 60000,
		func(_ context.Context, sessionID, model, _ string, _ int64) (*agent.DshSessionSummary, error) {
			return &agent.DshSessionSummary{SessionID: sessionID, Summary: "at the cap", Tokens: 4096, Model: model}, nil
		},
		func(_ context.Context, r dshSummaryRunRowRecord) { records = append(records, r) })
	*now = now.Add(90 * time.Second)
	scheduler.fire(t, 0)

	if len(records) != 1 || records[0].Decision != dshSummaryReasonProceed || records[0].SummaryTokens != 4096 {
		t.Fatalf("unexpected decisions: %#v", records)
	}
	if !summarySidecarExists(t, storeRoot, workDir, "session-1") {
		t.Fatal("a summary exactly at the cap must be stored")
	}
}
