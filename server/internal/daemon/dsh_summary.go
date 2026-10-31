package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/dshsession"
)

// S3-B1 (M-137): the dsh prompt-cache summary clock.
//
// When a dsh run ends, the daemon arms a per-session clock. At TTL−margin —
// while the provider's prompt cache is still warm — the clock decides from
// M-110 usage facts whether a summary is worth building, and if so invokes
// the bridge once (`summarize`) to run dsh's own compaction, then stores the
// summary beside the session. The clock lives in the daemon: an idle session
// never keeps a dsh process alive, and the bridge process exists only while
// the one summarize call runs.
//
// Every number here is config, not a constant (M-133): the spike could not
// measure DeepSeek's cache TTL without a real key, so the defaults below are
// the conservative published values and are re-measured after M-126.

const (
	// DefaultDshSummaryTokens is the summary size target: 2K tokens, the
	// Owner's 1–2K target (4K acceptable) leaning to its upper half because
	// dsh's compaction writes one replacement node, not a lossless digest.
	DefaultDshSummaryTokens = 2048
	// DefaultDshSummaryTokenCap is the hard ceiling: a summary above it is
	// rejected (decision summary-over-cap, nothing stored) rather than
	// truncated mid-sentence, and the next run starts fresh.
	DefaultDshSummaryTokenCap = 4096
	// DefaultDshSummaryTriggerTokens: conversation tokens above the init
	// prefix at which summarizing is worth it (~20K, M-133).
	DefaultDshSummaryTriggerTokens = 20000
	// DefaultDshSummaryTTL is the assumed provider cache TTL (1 h, the
	// conservative published value; DeepSeek reports ≥12 h warm in practice).
	DefaultDshSummaryTTL = time.Hour
	// DefaultDshSummaryMargin keeps this long enough before TTL that the
	// summarize call cannot race the cache going cold (15 min, M-133).
	DefaultDshSummaryMargin = 15 * time.Minute
	// DefaultDshSummaryColdWarmRatio is the cold/warm price ratio the cost
	// rule uses (~30× for v4-pro; 50× for v4-flash is the optimistic side).
	DefaultDshSummaryColdWarmRatio = 30.0
)

// DshPromptCacheConfig is one model's prompt-cache TTL shape — Pi's
// promptCache{short, long} shape, S3-B1. A cache entry's lifetime depends on
// prompt size: small prompts earn the short TTL, large ones the long TTL.
// The clock schedules against the longest TTL the model can see, so the
// summary is always built before the cache goes cold.
type DshPromptCacheConfig struct {
	Short time.Duration `json:"short"`
	Long  time.Duration `json:"long"`
}

// DshSummaryConfig is the whole summary clock configuration (S3-B1). Every
// field is operator-configurable; the defaults are the M-133 spike's
// provisional values, not measurements — do not hard-code them anywhere else.
type DshSummaryConfig struct {
	Enabled         bool
	SummaryTokens   int64
	SummaryTokenCap int64
	TriggerTokens   int64
	TTL             time.Duration
	Margin          time.Duration
	ColdWarmRatio   float64
	// PromptCache holds per-model TTL overrides keyed by the model id as the
	// run reported it (e.g. "deepseek/v4-pro").
	PromptCache map[string]DshPromptCacheConfig
}

// ttlForModel resolves the cache TTL assumption for one model: the model's
// configured long TTL wins, then its short TTL, then the daemon-wide
// assumption.
func (c DshSummaryConfig) ttlForModel(model string) time.Duration {
	if pc, ok := c.PromptCache[strings.TrimSpace(model)]; ok {
		if pc.Long > 0 {
			return pc.Long
		}
		if pc.Short > 0 {
			return pc.Short
		}
	}
	return c.TTL
}

// Decision reasons recorded on the run row. Stable strings: the server stores
// them verbatim in agent_task_queue.result.
const (
	dshSummaryReasonProceed   = "summarized"
	dshSummaryReasonBelow     = "skipped-below-trigger"
	dshSummaryReasonCost      = "skipped-cost"
	dshSummaryReasonFailed    = "summarize-failed"
	dshSummaryReasonNoBridge  = "skipped-bridge-unsupported"
	dshSummaryReasonStoreFail = "store-failed"
	dshSummaryReasonOverCap   = "summary-over-cap"
)

// dshSummaryVerdict is one clock decision.
type dshSummaryVerdict struct {
	Proceed bool
	Reason  string
}

// decideDshSummary is the pure trigger+cost decision (S3-B1).
//
// Trigger: conversation tokens above the init prefix reached the configured
// threshold. The decision is token-only: the clock fires at TTL−margin — with
// the default 1 h TTL and 15 min margin, 45 min after the run ended — so an
// idle clause would be either always true (idle trigger shorter than the
// fire delay) or dead config; the earlier "20K or 30 min idle" shape was
// dropped for exactly that reason (Architect review of 232e8ec).
//
// Cost rule: at TTL−margin the provider cache is still warm, so building the
// summary re-reads the conversation at warm prices plus one short completion;
// skipping leaves the next resume to re-read it cold at ColdWarmRatio×. Both
// paths pay one full re-read of the conversation, so the re-read cancels out
// and the comparison is the per-token cold-vs-warm price gap against the fixed
// cost of producing SummaryTokens of new output. Summarize when the gap the
// summary banks is at least the summary's own production cost — i.e. when
// tokensAboveInit × (ColdWarmRatio − 1) ≥ SummaryTokens. A cold/warm ratio of
// 1 (operator disables the cache's price advantage) therefore never
// summarizes, and every session above the trigger summarizes once the
// conversation clears SummaryTokens/(ColdWarmRatio−1) tokens.
func decideDshSummary(tokensAboveInit int64, cfg DshSummaryConfig) dshSummaryVerdict {
	if tokensAboveInit < cfg.TriggerTokens {
		return dshSummaryVerdict{Reason: dshSummaryReasonBelow}
	}
	gap := float64(tokensAboveInit) * (cfg.ColdWarmRatio - 1)
	summaryCost := float64(cfg.SummaryTokens)
	if gap < summaryCost {
		return dshSummaryVerdict{Reason: dshSummaryReasonCost}
	}
	return dshSummaryVerdict{Proceed: true, Reason: dshSummaryReasonProceed}
}

// dshSessionSummaryRecord is the sidecar stored beside the summarized session
// (S3-B1): what was built, how big, when, and which session it came from.
type dshSessionSummaryRecord struct {
	Summary       string    `json:"summary"`
	Tokens        int64     `json:"tokens"`
	MadeAt        time.Time `json:"made_at"`
	ParentSession string    `json:"parent_session"`
}

// dshSummarySidecarName is the file the record lands as inside the session's
// own store directory — beside the session, never inside its event log.
const dshSummarySidecarName = "summary.json"

// dshSessionInitPrefixSidecarName stores the session's init-prefix size (the
// first run's FirstPromptTokens) beside the session, so a later resumed run
// can measure growth above the init prefix instead of only its own run.
const dshSessionInitPrefixSidecarName = "init_prefix.json"

type dshSessionInitPrefixRecord struct {
	Tokens int64     `json:"tokens"`
	MadeAt time.Time `json:"made_at,omitempty"`
}

// writeDshSummarySidecar stores record at
// <storeRoot>/<projectKey(cwd)>/<sessionID>/summary.json — the dsh session
// store layout pkg/dshsession mirrors. The write is temp-file + rename so a
// daemon crash can never leave a half-written summary for a later reader.
func writeDshSummarySidecar(storeRoot, cwd, sessionID string, record dshSessionSummaryRecord) error {
	if strings.TrimSpace(storeRoot) == "" {
		return errors.New("dsh summary store root is empty")
	}
	if !dshsession.ValidateSessionID(sessionID) {
		return fmt.Errorf("dsh summary: session id %q cannot name a store directory", sessionID)
	}
	dir := filepath.Join(storeRoot, dshsession.ProjectKey(cwd), sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dsh summary dir: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dsh summary: %w", err)
	}
	final := filepath.Join(dir, dshSummarySidecarName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write dsh summary: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish dsh summary: %w", err)
	}
	return nil
}

// writeDshSessionInitPrefix stores tokens at
// <storeRoot>/<projectKey(cwd)>/<sessionID>/init_prefix.json, only when no
// value is already recorded — the first run that creates the session owns
// the init prefix and later (resumed) runs must not overwrite it.
func writeDshSessionInitPrefix(storeRoot, cwd, sessionID string, tokens int64) error {
	if strings.TrimSpace(storeRoot) == "" {
		return errors.New("dsh init prefix store root is empty")
	}
	if !dshsession.ValidateSessionID(sessionID) {
		return fmt.Errorf("dsh init prefix: session id %q cannot name a store directory", sessionID)
	}
	if tokens <= 0 {
		return errors.New("dsh init prefix: tokens must be positive")
	}
	dir := filepath.Join(storeRoot, dshsession.ProjectKey(cwd), sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dsh init prefix dir: %w", err)
	}
	final := filepath.Join(dir, dshSessionInitPrefixSidecarName)
	if _, err := os.Stat(final); err == nil {
		return nil // already recorded; keep the first run's value
	}
	record := dshSessionInitPrefixRecord{Tokens: tokens, MadeAt: time.Now()}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dsh init prefix: %w", err)
	}
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write dsh init prefix: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish dsh init prefix: %w", err)
	}
	return nil
}

// readDshSessionInitPrefix reads the persisted init-prefix size. Returns an
// error when no value is recorded (older sessions), so callers can fall back
// to the run's own FirstPromptTokens.
func readDshSessionInitPrefix(storeRoot, cwd, sessionID string) (int64, error) {
	if strings.TrimSpace(storeRoot) == "" {
		return 0, errors.New("dsh init prefix store root is empty")
	}
	if !dshsession.ValidateSessionID(sessionID) {
		return 0, fmt.Errorf("dsh init prefix: session id %q cannot name a store directory", sessionID)
	}
	path := filepath.Join(storeRoot, dshsession.ProjectKey(cwd), sessionID, dshSessionInitPrefixSidecarName)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var record dshSessionInitPrefixRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return 0, err
	}
	if record.Tokens <= 0 {
		return 0, errors.New("dsh init prefix: zero tokens")
	}
	return record.Tokens, nil
}

// estimateDshSummaryTokens is the rough tokens estimate (bytes/4) used when
// the bridge did not report a count. Good enough for a decision record; the
// trigger decisions themselves use M-110 usage facts, never this estimate.
func estimateDshSummaryTokens(summary string) int64 {
	return int64((len(summary) + 3) / 4)
}

// dshSummaryRunRowRecord is the per-run decision log (S3-B1: "log each
// decision on the run row"). The daemon posts it to the server, which merges
// it into agent_task_queue.result under the dsh_summary key.
type dshSummaryRunRowRecord struct {
	Decision        string `json:"decision"`
	SessionID       string `json:"session_id"`
	Model           string `json:"model,omitempty"`
	TokensAboveInit int64  `json:"tokens_above_init"`
	IdleSeconds     int64  `json:"idle_seconds"`
	SummaryTokens   int64  `json:"summary_tokens,omitempty"`
	MadeAt          string `json:"made_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// dshSessionSummarizer is the one method the clock needs from the resolved
// dsh backend. Type-asserting it is the daemon-side capability gate: no dsh
// backend, no clock.
type dshSessionSummarizer interface {
	SummarizeSession(ctx context.Context, sessionID, model, cwd string, maxTokens int64) (*agent.DshSessionSummary, error)
}

// dshSummaryRequest is what a completed dsh run hands the clock. The two
// token facts are the run's first and last model-request prompt sizes
// (input + cache_read + cache_write, M-110 usage facts in frame order) from
// result.FirstPromptTokens/LastPromptTokens; their difference is the
// conversation growth the summary decision runs on, so a one-shot run can
// reach the trigger on its own.
type dshSummaryRequest struct {
	TaskID            string
	SessionID         string
	WorkDir           string
	StoreRoot         string
	Model             string
	FirstPromptTokens int64 // prompt tokens of the run's first model request
	LastPromptTokens  int64 // prompt tokens of the run's last model request
	// PriorSessionID is non-empty when this run resumed an existing session;
	// in that case the init prefix is owned by the session's first run and
	// must not be (re)written here.
	PriorSessionID string
	// EndedAt is when the run ended. Zero means "just now" (the live path);
	// set, it anchors the fire time to the run's end instead of the arm time,
	// which is what re-arming after a daemon restart needs.
	EndedAt        time.Time
	Summarize      func(ctx context.Context, sessionID, model, workDir string, maxTokens int64) (*agent.DshSessionSummary, error)
	RecordDecision func(ctx context.Context, record dshSummaryRunRowRecord)
}

// dshSummaryScheduler schedules one delayed callback and returns its cancel
// func. Production is time.AfterFunc; tests replace it to drive the clock
// deterministically with a fake.
type dshSummaryScheduler interface {
	Schedule(delay time.Duration, fire func()) (cancel func())
}

type timeAfterFuncScheduler struct{}

func (timeAfterFuncScheduler) Schedule(delay time.Duration, fire func()) func() {
	timer := time.AfterFunc(delay, fire)
	return func() { _ = timer.Stop() }
}

// dshSummaryClock is the daemon-resident per-session cache clock (S3-B1).
// Arm replaces any pending fire for the session (a newer run has newer usage
// facts); Cancel disarms it — a session that is being resumed again must not
// be compacted mid-conversation. The clock keeps no per-session state beyond
// the pending timers themselves: every fact the fire decision needs rides the
// request, so nothing accumulates per session.
type dshSummaryClock struct {
	mu        sync.Mutex
	cfg       DshSummaryConfig
	now       func() time.Time
	scheduler dshSummaryScheduler
	cancels   map[string]dshArmedTimer
	log       *slog.Logger
	// ctxFn resolves the daemon-lifetime context the summarize call runs
	// under. The task context is already cancelled when the clock fires, so
	// it can never be the one; New wires this to the daemon's root context.
	ctxFn func() context.Context
}

func newDshSummaryClock(cfg DshSummaryConfig, log *slog.Logger) *dshSummaryClock {
	return &dshSummaryClock{
		cfg:       cfg,
		now:       time.Now,
		scheduler: timeAfterFuncScheduler{},
		cancels:   make(map[string]dshArmedTimer),
		log:       log,
		ctxFn:     func() context.Context { return context.Background() },
	}
}

// dshArmedTimer is one pending fire plus the arm record that lets a restarted
// daemon re-arm it; Cancel and the replacing Arm remove that record.
type dshArmedTimer struct {
	cancel  func()
	armPath string
}

// dueIn is how long until the summarize point (TTL−margin after the run
// ended) for a model; non-positive once that point has passed.
func (c *dshSummaryClock) dueIn(model string, endedAt time.Time) time.Duration {
	return endedAt.Add(c.cfg.ttlForModel(model) - c.cfg.Margin).Sub(c.now())
}

func (c *dshSummaryClock) context() context.Context {
	if c == nil || c.ctxFn == nil {
		return context.Background()
	}
	return c.ctxFn()
}

// Arm starts (or restarts) the per-session clock. No-op when the feature is
// disabled, the session is empty, or the backend cannot summarize — the zero
// values here are the common case for every non-dsh provider.
func (c *dshSummaryClock) Arm(req dshSummaryRequest) {
	if c == nil || !c.cfg.Enabled {
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || req.Summarize == nil {
		return
	}
	if req.PriorSessionID == "" && req.FirstPromptTokens > 0 && strings.TrimSpace(req.StoreRoot) != "" {
		if _, err := readDshSessionInitPrefix(req.StoreRoot, req.WorkDir, req.SessionID); err != nil {
			_ = writeDshSessionInitPrefix(req.StoreRoot, req.WorkDir, req.SessionID, req.FirstPromptTokens)
		}
	}
	delay := c.cfg.ttlForModel(req.Model) - c.cfg.Margin
	if delay <= 0 {
		c.log.Warn("dsh summary clock not armed: margin is not shorter than the TTL",
			"session_id", req.SessionID, "ttl", c.cfg.ttlForModel(req.Model), "margin", c.cfg.Margin)
		return
	}
	armPath := dshArmRecordPath(req.StoreRoot, req.WorkDir, req.SessionID)
	armedAt := c.now()
	if !req.EndedAt.IsZero() {
		armedAt = req.EndedAt
		delay = c.dueIn(req.Model, req.EndedAt)
		if delay <= 0 {
			c.log.Info("dsh summary clock not armed: the run ended past the summarize point",
				"task_id", req.TaskID, "session_id", req.SessionID, "ended_at", req.EndedAt)
			removeDshArmRecord(armPath)
			return
		}
	}
	if armPath != "" {
		if err := writeDshArmRecord(armPath, dshArmRecord{
			TaskID:            req.TaskID,
			WorkDir:           req.WorkDir,
			Model:             req.Model,
			EndedAt:           armedAt,
			FirstPromptTokens: req.FirstPromptTokens,
			LastPromptTokens:  req.LastPromptTokens,
			PriorSessionID:    req.PriorSessionID,
		}); err != nil {
			c.log.Warn("dsh summary arm record not written; the clock will not survive a daemon restart",
				"session_id", req.SessionID, "error", err)
		}
	}
	c.mu.Lock()
	if prior, ok := c.cancels[req.SessionID]; ok {
		prior.cancel()
		if prior.armPath != armPath {
			removeDshArmRecord(prior.armPath)
		}
	}
	c.cancels[req.SessionID] = dshArmedTimer{
		armPath: armPath,
		cancel: c.scheduler.Schedule(delay, func() {
			c.fire(req, armedAt)
		}),
	}
	c.mu.Unlock()
	c.log.Debug("dsh summary clock armed",
		"task_id", req.TaskID, "session_id", req.SessionID, "model", req.Model,
		"fire_in", delay.String(), "first_prompt_tokens", req.FirstPromptTokens,
		"last_prompt_tokens", req.LastPromptTokens)
}

// Cancel disarms the clock for one session — the session is being resumed, so
// summarizing it mid-conversation would be wrong.
func (c *dshSummaryClock) Cancel(sessionID string) {
	if c == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	c.mu.Lock()
	armed, ok := c.cancels[sessionID]
	delete(c.cancels, sessionID)
	c.mu.Unlock()
	if ok {
		armed.cancel()
		removeDshArmRecord(armed.armPath)
		c.log.Debug("dsh summary clock cancelled: session resumed", "session_id", sessionID)
	}
}

// fire is the clock callback: decide, and on a proceed verdict invoke the
// bridge once and store the summary beside the session. Every outcome lands
// in the run's decision record.
func (c *dshSummaryClock) fire(req dshSummaryRequest, armedAt time.Time) {
	c.mu.Lock()
	delete(c.cancels, req.SessionID)
	c.mu.Unlock()
	removeDshArmRecord(dshArmRecordPath(req.StoreRoot, req.WorkDir, req.SessionID))

	// A session that already has a summary is never summarized again; this is
	// what keeps a re-armed clock from overwriting one built before a restart.
	if dshSummarySidecarExists(req.StoreRoot, req.WorkDir, req.SessionID) {
		c.log.Debug("dsh summary clock skipped: session already has a summary",
			"task_id", req.TaskID, "session_id", req.SessionID)
		return
	}

	now := c.now()
	idle := now.Sub(armedAt)
	if idle < 0 {
		idle = 0
	}
	// Conversation growth above the init prefix. The init prefix is the
	// session's first run's FirstPromptTokens, persisted beside the session;
	// a resumed run's own FirstPromptTokens already carries the whole history,
	// so it cannot be used. Fall back to the run's FirstPromptTokens when no
	// persisted value exists (older sessions).
	initPrefix := req.FirstPromptTokens
	if persisted, err := readDshSessionInitPrefix(req.StoreRoot, req.WorkDir, req.SessionID); err == nil && persisted > 0 {
		initPrefix = persisted
	}
	// Conversation growth above the init prefix, measured as the last model
	// request's prompt size minus the init prefix — context size, not billed
	// volume, so a one-shot run can reach the trigger by itself.
	tokensAboveInit := req.LastPromptTokens - initPrefix
	if tokensAboveInit < 0 {
		tokensAboveInit = 0
	}
	verdict := decideDshSummary(tokensAboveInit, c.cfg)
	record := dshSummaryRunRowRecord{
		Decision:        verdict.Reason,
		SessionID:       req.SessionID,
		Model:           req.Model,
		TokensAboveInit: tokensAboveInit,
		IdleSeconds:     int64(idle.Seconds()),
	}
	c.log.Info("dsh summary clock fired",
		"task_id", req.TaskID, "session_id", req.SessionID, "decision", verdict.Reason,
		"tokens_above_init", tokensAboveInit, "idle", idle.String())
	if !verdict.Proceed {
		c.record(req, record)
		return
	}

	summary, err := req.Summarize(c.context(), req.SessionID, req.Model, req.WorkDir, c.cfg.SummaryTokenCap)
	if err != nil {
		record.Decision = dshSummaryReasonFailed
		record.Error = err.Error()
		if errors.Is(err, agent.ErrDshSummarizeUnsupported) {
			record.Decision = dshSummaryReasonNoBridge
		}
		if errors.Is(err, agent.ErrDshSummaryOverCap) {
			record.Decision = dshSummaryReasonOverCap
		}
		c.log.Warn("dsh summary build failed", "task_id", req.TaskID,
			"session_id", req.SessionID, "decision", record.Decision, "error", err)
		c.record(req, record)
		return
	}
	tokens := summary.Tokens
	if tokens <= 0 {
		tokens = estimateDshSummaryTokens(summary.Summary)
	}
	// The bridge enforces the budget only when it advertises
	// summarize_max_tokens; this check covers every other bridge. An over-cap
	// summary is never stored, so the next run starts fresh.
	if c.cfg.SummaryTokenCap > 0 && tokens > c.cfg.SummaryTokenCap {
		record.Decision = dshSummaryReasonOverCap
		record.SummaryTokens = tokens
		c.log.Warn("dsh summary exceeds the token cap; not stored",
			"task_id", req.TaskID, "session_id", req.SessionID,
			"tokens", tokens, "cap", c.cfg.SummaryTokenCap)
		c.record(req, record)
		return
	}
	stored := dshSessionSummaryRecord{
		Summary:       summary.Summary,
		Tokens:        tokens,
		MadeAt:        now,
		ParentSession: req.SessionID,
	}
	if err := writeDshSummarySidecar(req.StoreRoot, req.WorkDir, req.SessionID, stored); err != nil {
		record.Decision = dshSummaryReasonStoreFail
		record.Error = err.Error()
		c.log.Warn("dsh summary store failed", "task_id", req.TaskID,
			"session_id", req.SessionID, "decision", record.Decision, "error", err)
		c.record(req, record)
		return
	}
	record.Decision = dshSummaryReasonProceed
	record.SummaryTokens = tokens
	record.MadeAt = now.Format(time.RFC3339)
	c.log.Info("dsh summary stored beside session",
		"task_id", req.TaskID, "session_id", req.SessionID, "tokens", tokens)
	c.record(req, record)
}

func (c *dshSummaryClock) record(req dshSummaryRequest, record dshSummaryRunRowRecord) {
	if req.RecordDecision == nil {
		return
	}
	req.RecordDecision(c.context(), record)
}

// dshSharedSessionStoreRoot resolves the shared dsh store the bridge writes
// into when MESSAGE_SINK=dsh leaves MULTICA_DSH_SESSION_ROOT unset:
// $DSH_HOME/sessions, defaulting to ~/.dsh/sessions (the same resolution
// local_skills.go and dshMulticaProfileManifestPath use).
func dshSharedSessionStoreRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dshHome := strings.TrimSpace(os.Getenv("DSH_HOME"))
	if dshHome == "" {
		dshHome = filepath.Join(home, ".dsh")
	}
	return filepath.Join(dshHome, "sessions")
}

// promptCacheFromEnv parses the per-model promptCache map from
// MULTICA_DSH_PROMPT_CACHE, a JSON object of model id → {short, long}
// duration strings, e.g.
//
//	{"deepseek/v4-pro": {"short": "5m", "long": "1h"}}
//
// A malformed map is a configuration error, not a silent fallback: an
// operator who typed it wants to know it was not applied.
func promptCacheFromEnv(name string) (map[string]DshPromptCacheConfig, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	var entries map[string]struct {
		Short string `json:"short"`
		Long  string `json:"long"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	out := make(map[string]DshPromptCacheConfig, len(entries))
	for model, entry := range entries {
		key := strings.TrimSpace(model)
		if key == "" {
			return nil, fmt.Errorf("%s: empty model key", name)
		}
		var pc DshPromptCacheConfig
		if text := strings.TrimSpace(entry.Short); text != "" {
			parsed, err := parseFlexDuration(text)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("%s: model %q has invalid short duration %q", name, key, text)
			}
			pc.Short = parsed
		}
		if text := strings.TrimSpace(entry.Long); text != "" {
			parsed, err := parseFlexDuration(text)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("%s: model %q has invalid long duration %q", name, key, text)
			}
			pc.Long = parsed
		}
		if pc.Short <= 0 && pc.Long <= 0 {
			return nil, fmt.Errorf("%s: model %q sets neither short nor long", name, key)
		}
		out[key] = pc
	}
	return out, nil
}

// dshSummaryConfigFromEnv loads the summary clock configuration with
// default > env precedence. MULTICA_DSH_SUMMARY_ENABLED=0 is the kill
// switch; everything else is a tuning knob.
func dshSummaryConfigFromEnv() (DshSummaryConfig, error) {
	enabled := true
	if raw := strings.TrimSpace(os.Getenv("MULTICA_DSH_SUMMARY_ENABLED")); raw != "" {
		switch strings.ToLower(raw) {
		case "true", "1", "yes", "on":
			enabled = true
		case "false", "0", "no", "off":
			enabled = false
		default:
			return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_ENABLED must be a boolean, got %q", raw)
		}
	}
	cfg := DshSummaryConfig{
		Enabled:         enabled,
		SummaryTokens:   DefaultDshSummaryTokens,
		SummaryTokenCap: DefaultDshSummaryTokenCap,
		TriggerTokens:   DefaultDshSummaryTriggerTokens,
		TTL:             DefaultDshSummaryTTL,
		Margin:          DefaultDshSummaryMargin,
		ColdWarmRatio:   DefaultDshSummaryColdWarmRatio,
	}
	var err error
	if cfg.SummaryTokens, err = int64FromEnv("MULTICA_DSH_SUMMARY_TOKENS", cfg.SummaryTokens); err != nil {
		return DshSummaryConfig{}, err
	}
	if cfg.SummaryTokenCap, err = int64FromEnv("MULTICA_DSH_SUMMARY_TOKEN_CAP", cfg.SummaryTokenCap); err != nil {
		return DshSummaryConfig{}, err
	}
	if cfg.TriggerTokens, err = int64FromEnv("MULTICA_DSH_SUMMARY_TRIGGER_TOKENS", cfg.TriggerTokens); err != nil {
		return DshSummaryConfig{}, err
	}
	if cfg.TTL, err = durationFromEnv("MULTICA_DSH_SUMMARY_TTL", cfg.TTL); err != nil {
		return DshSummaryConfig{}, err
	}
	if cfg.Margin, err = durationFromEnv("MULTICA_DSH_SUMMARY_MARGIN", cfg.Margin); err != nil {
		return DshSummaryConfig{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_DSH_SUMMARY_COLD_WARM_RATIO")); raw != "" {
		ratio, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil || ratio <= 0 {
			return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_COLD_WARM_RATIO must be a positive number, got %q", raw)
		}
		cfg.ColdWarmRatio = ratio
	}
	if cfg.PromptCache, err = promptCacheFromEnv("MULTICA_DSH_PROMPT_CACHE"); err != nil {
		return DshSummaryConfig{}, err
	}
	if cfg.TTL <= 0 {
		return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_TTL must be positive, got %s", cfg.TTL)
	}
	if cfg.Margin < 0 {
		return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_MARGIN must be non-negative, got %s", cfg.Margin)
	}
	if cfg.Margin >= cfg.TTL {
		return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_MARGIN must be shorter than the TTL, got margin=%s ttl=%s", cfg.Margin, cfg.TTL)
	}
	for model, pc := range cfg.PromptCache {
		ttl := pc.Long
		if ttl <= 0 {
			ttl = pc.Short
		}
		if ttl <= 0 {
			continue
		}
		if cfg.Margin >= ttl {
			return DshSummaryConfig{}, fmt.Errorf("MULTICA_DSH_SUMMARY_MARGIN must be shorter than the TTL for model %q, got margin=%s ttl=%s", model, cfg.Margin, ttl)
		}
	}
	return cfg, nil
}

// int64FromEnv reads a non-negative integer override.
func int64FromEnv(name string, def int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", name, raw)
	}
	return value, nil
}

// armDshSummaryClock arms the per-session clock when a dsh run ends (S3-B1).
// The trigger facts are the run's first and last model-request prompt sizes
// from M-110 usage on the result (the dsh backend records them in frame
// order from its per-request usage frames); their difference is the
// conversation growth the fire decision runs on. No-op for every other
// provider and for a backend that cannot summarize.
func (d *Daemon) armDshSummaryClock(task Task, provider, model string, result agent.Result, workDir, storeRoot string, backend agent.Backend) {
	if provider != "dsh" || strings.TrimSpace(result.SessionID) == "" {
		return
	}
	summarizer, ok := backend.(dshSessionSummarizer)
	if !ok {
		d.logger.Debug("dsh summary clock not armed: backend cannot summarize",
			"task_id", task.ID, "session_id", result.SessionID)
		return
	}
	d.dshSummary.Arm(dshSummaryRequest{
		TaskID:            task.ID,
		SessionID:         result.SessionID,
		WorkDir:           workDir,
		StoreRoot:         storeRoot,
		Model:             model,
		PriorSessionID:    task.PriorSessionID,
		FirstPromptTokens: result.FirstPromptTokens,
		LastPromptTokens:  result.LastPromptTokens,
		Summarize: func(ctx context.Context, sessionID, model, workDir string, maxTokens int64) (*agent.DshSessionSummary, error) {
			return summarizer.SummarizeSession(ctx, sessionID, model, workDir, maxTokens)
		},
		RecordDecision: d.recordDshSummaryDecision(task.ID),
	})
}

// recordDshSummaryDecision posts each clock decision to the server, which
// merges it into the run row's result JSONB. A server that predates the
// endpoint (404) only loses the record — the daemon logs and moves on rather
// than retrying a write it can never land.
func (d *Daemon) recordDshSummaryDecision(taskID string) func(context.Context, dshSummaryRunRowRecord) {
	return func(ctx context.Context, record dshSummaryRunRowRecord) {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := d.client.RecordTaskSummaryDecision(recordCtx, taskID, record); err != nil {
			var reqErr *requestError
			if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound {
				return
			}
			d.logger.Debug("dsh summary decision not recorded", "task_id", taskID, "error", err)
		}
	}
}
