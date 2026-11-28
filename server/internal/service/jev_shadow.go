package service

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/jev"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Jev shadow mode (M-574 A4 / M-580).
//
// The orchestration decisions that cost a model run are not made by code: a
// wakeup fires, an agent run starts, and the run decides whether the card needs
// anything. The measurable question is whether that run was worth starting —
// "will this check-in find something to do, or will it check in and find
// nothing?" — and shadow mode answers it without changing anything: Jev's
// answer is logged next to the dispatch that already happens, and the product
// behaves exactly as it does with the flag off.
//
// Three properties make that a real observation rather than a decoration:
//
//   - the state is captured *before* the dispatch, so the answer is a
//     prediction of the run rather than a report on it;
//   - nothing here can fail a dispatch: every path returns, and an error only
//     becomes a log line and a row with error_kind;
//   - with no flag and no key there is no client, no query and no HTTP call.
//
// This file holds no policy on purpose. The threshold below is the reading of
// the answer for the log row; what any of it means for a live cutover is a
// decision nobody has made yet.

const (
	// jevShadowDecisionPointWakeup names the wakeup-dispatch fork (M-580).
	jevShadowDecisionPointWakeup = "issue_wakeup_dispatch"
	// jevShadowDecisionPoint names the fork this shadow observes. It stays
	// the wakeup dispatch for the default path; other forks pass their own
	// point through the job.
	jevShadowDecisionPoint = jevShadowDecisionPointWakeup
	// jevShadowSubjectType is what the question is about: one wakeup rule.
	jevShadowSubjectType = "issue_wakeup"
	// jevShadowQuestionKey is the id the answer comes back under.
	jevShadowQuestionKey = "needs_action"
	// jevShadowDecisionPointReclaim names the stall-resume fork (M-592): a
	// stale-dispatched task the claim path re-delivers. Task-level recovery
	// of a dispatched, never-started task — not the Owner's card-level
	// "resume this stalled card". Only reclaimed tasks produce rows, so
	// agreement here measures calibration of "will it start and act", with
	// no negative class.
	jevShadowDecisionPointReclaim = "stall_resume_reclaim"
	// jevShadowSubjectTypeTask is what the reclaim question is about: one task.
	jevShadowSubjectTypeTask = "agent_task_queue"
	// jevShadowQuestionKeyResume is the id the reclaim answer comes back under.
	jevShadowQuestionKeyResume = "needs_resume"
	// jevShadowDecisionPointRoute names the triage-routing fork (M-592): an
	// agent-role assign that picks which route fits the card.
	jevShadowDecisionPointRoute = "triage_route_assign"
	// jevShadowQuestionKeyRoute is the id the routing answer comes back under.
	jevShadowQuestionKeyRoute = "route_pick"
	// jevShadowThreshold splits "the run will act" from "the run will find
	// nothing". It is the log's reading of the answer, not a gate.
	jevShadowThreshold = 0.5
	// jevShadowWorkers bounds how many answers are in flight at once. A fixed
	// pool rather than a goroutine per dispatch: a slow endpoint must cost
	// dropped observations, never goroutines.
	jevShadowWorkers = 4
	// jevShadowQueue bounds how many dispatches may wait for a worker.
	jevShadowQueue = 64
	// jevShadowFlag turns the observation on. Off by default, and the only way
	// a dispatch is ever affected is by the log line that says it is off.
	jevShadowFlag = "MULTICA_JEV_SHADOW"
	// jevShadowMaxPerHourEnv caps vendor calls in a rolling clock hour. The
	// shadow is a sample, not a firehose: a cap keeps a noisy schedule from
	// spending the whole free-tier quota on observations. A missing or invalid
	// value takes the default; a value of zero is invalid, since turning the
	// shadow off is the flag's job.
	jevShadowMaxPerHourEnv     = "MULTICA_JEV_SHADOW_MAX_PER_HOUR"
	jevShadowMaxPerHourDefault = 120
	// jevShadowRateLimitedKind is recorded instead of the generic "http" when
	// the endpoint answered 429. The agreement report can then separate a
	// spent quota from a broken endpoint.
	jevShadowRateLimitedKind = "rate_limited"
	// jevShadowBreakerDefault is the pause after a 429 with no usable
	// Retry-After; jevShadowBreakerMax bounds an absurdly long header value.
	jevShadowBreakerDefault = time.Hour
	jevShadowBreakerMax     = 24 * time.Hour
)

// WakeupDispatchSnapshot is the structured state sent to the vendor, captured
// before the dispatch. It carries metadata only: no comment bodies, no wakeup
// instruction text, no titles. That boundary is deliberate — it is what the
// Owner approves with the key — so nothing here may grow a free-text field.
type WakeupDispatchSnapshot struct {
	WorkspaceID string
	WakeupID    string
	IssueID     string

	Kind          string
	Mode          string
	ConditionType string
	SystemRule    string
	IsSystem      bool

	IssueStatus      string
	AssigneeType     string
	AssigneeID       string
	IdleMinutes      int
	HasLiveRun       bool
	OtherLiveWakeups int
}

// State is the JSON body sent as the call's state: structured metadata about
// the rule and the card, and nothing a person wrote.
func (s *WakeupDispatchSnapshot) State() map[string]any {
	return map[string]any{
		"decision": jevShadowDecisionPoint,
		"wakeup": map[string]any{
			"kind":           s.Kind,
			"mode":           s.Mode,
			"is_system_rule": s.IsSystem,
			"system_rule":    s.SystemRule,
			"condition_type": s.ConditionType,
		},
		"issue": map[string]any{
			"status":             s.IssueStatus,
			"assignee_type":      s.AssigneeType,
			"assignee_id":        s.AssigneeID,
			"idle_minutes":       s.IdleMinutes,
			"has_live_run":       s.HasLiveRun,
			"other_live_wakeups": s.OtherLiveWakeups,
		},
	}
}

// JevShadow observes wakeup dispatches. A nil receiver is valid everywhere and
// means "shadow mode is off", so the call site needs no branch for the default
// configuration.
type JevShadow struct {
	client *jev.Client
	db     db.DBTX
	log    *slog.Logger
	jobs   chan jevShadowJob
	// inFlight counts enqueued observations so tests can wait for them; the
	// product never does.
	inFlight sync.WaitGroup
	dropped  atomic.Int64
	now      func() time.Time
	// budget and breaker gate the vendor call itself. Nil is safe: a shadow
	// built directly in a test can opt out of both.
	budget  *shadowBudget
	breaker *shadowBreaker
}

type jevShadowJob struct {
	state         map[string]any
	workspaceID   string
	subjectID     string
	issueID       string
	decisionPoint string
	subjectType   string
	questionKey   string
	// question carries the pre-built vendor question (noul or choice).
	question jev.Question
	// verdictFor maps an answer to its verdict string. Observe keeps one
	// shared path; each fork supplies its own reading of the answer.
	verdictFor func(jev.Answer) string
	// threshold is NULL for choice rows (no split applies); set for noul.
	threshold *float64
	// logKey names the subject in debug lines ("wakeup_id" or "task_id").
	logKey string
}

// shadowBudget caps vendor calls in a rolling clock hour. It is checked at the
// moment of the call, not at enqueue time, so a job that waited behind a slow
// endpoint cannot spend quota after the window rolled. Refusals are counted in
// memory only: writing a row for each would put the noise back in the table the
// cap exists to keep quiet.
type shadowBudget struct {
	max int64
	mu  sync.Mutex
	// calls holds the timestamps of accepted calls in the last hour, oldest
	// first. A rolling window rather than a fixed clock hour: 120 calls just
	// before the hour turns and 120 just after would otherwise be 240 in one
	// real hour, which is exactly the quota burn the cap exists to stop.
	calls   []time.Time
	skipped atomic.Int64
}

func newShadowBudget(max int64) *shadowBudget {
	if max <= 0 {
		max = jevShadowMaxPerHourDefault
	}
	return &shadowBudget{max: max}
}

// shadowBudgetFromEnv reads the cap once at startup. A missing, malformed or
// non-positive value takes the default rather than silently disabling calls.
func shadowBudgetFromEnv() (*shadowBudget, int64) {
	max := int64(jevShadowMaxPerHourDefault)
	if raw := strings.TrimSpace(os.Getenv(jevShadowMaxPerHourEnv)); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			max = parsed
		}
	}
	return newShadowBudget(max), max
}

// allow consumes one slot when the last rolling hour still has room.
func (b *shadowBudget) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := now.Add(-time.Hour)
	stale := 0
	for stale < len(b.calls) && !b.calls[stale].After(cutoff) {
		stale++
	}
	if stale > 0 {
		b.calls = append([]time.Time(nil), b.calls[stale:]...)
	}
	if int64(len(b.calls)) >= b.max {
		b.skipped.Add(1)
		return false
	}
	b.calls = append(b.calls, now)
	return true
}

func (b *shadowBudget) skippedCalls() int64 { return b.skipped.Load() }

// shadowBreaker pauses every shadow call after a 429. It opens on the first
// rate limit and only ever extends its deadline, so a second 429 cannot shorten
// a window a previous header asked for.
type shadowBreaker struct {
	mu      sync.Mutex
	until   time.Time
	skipped atomic.Int64
}

// allow reports whether calls may go out now, counting every refusal.
func (b *shadowBreaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.until) {
		b.skipped.Add(1)
		return false
	}
	return true
}

// trip opens the breaker for pause, or for the default when the endpoint gave
// no usable Retry-After. An absurdly long header is clamped.
func (b *shadowBreaker) trip(now time.Time, pause time.Duration) {
	if pause <= 0 {
		pause = jevShadowBreakerDefault
	}
	if pause > jevShadowBreakerMax {
		pause = jevShadowBreakerMax
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if until := now.Add(pause); until.After(b.until) {
		b.until = until
	}
}

func (b *shadowBreaker) skippedCalls() int64 { return b.skipped.Load() }

// StartJevShadow reads the flag and the key and returns nil unless both say a
// shadow is possible. It logs once, so an operator reading the startup logs
// knows which of the two switched it off rather than wondering why no rows
// appear.
func StartJevShadow(ctx context.Context, database db.DBTX, log *slog.Logger) *JevShadow {
	if log == nil {
		log = slog.Default()
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(jevShadowFlag))) {
	case "wakeup", "on", "1", "true", "yes":
	default:
		log.Info("jev shadow: off", "flag", jevShadowFlag)
		return nil
	}
	cfg, ok := jev.ConfigFromEnv(os.Getenv)
	if !ok {
		log.Info("jev shadow: off", "reason", "no API key", "key_env", jev.DefaultAPIKeyEnv)
		return nil
	}
	client, err := jev.NewClient(cfg)
	if err != nil {
		log.Info("jev shadow: off", "reason", "unusable jev configuration", "detail", err.Error())
		return nil
	}
	log.Info("jev shadow: on", "decision_point", jevShadowDecisionPoint, "model", cfg.Model, "api_base", cfg.APIBase)
	shadow := newJevShadow(ctx, database, client, log)
	log.Info("jev shadow: call budget", "env", jevShadowMaxPerHourEnv, "max_per_hour", shadow.budget.max)
	return shadow
}

// newJevShadow starts the worker pool that answers observations. Callers hand it
// a context that is cancelled at shutdown, which is what stops the pool.
func newJevShadow(ctx context.Context, database db.DBTX, client *jev.Client, log *slog.Logger) *JevShadow {
	now := time.Now
	budget, _ := shadowBudgetFromEnv()
	shadow := &JevShadow{
		client:  client,
		db:      database,
		log:     log,
		jobs:    make(chan jevShadowJob, jevShadowQueue),
		now:     now,
		budget:  budget,
		breaker: &shadowBreaker{},
	}
	for i := 0; i < jevShadowWorkers; i++ {
		go shadow.work(ctx)
	}
	return shadow
}

// Enabled reports whether this shadow will observe anything. A nil shadow, the
// default configuration, is disabled.
func (s *JevShadow) Enabled() bool { return s != nil && s.client != nil }

// Dropped counts observations discarded because the queue was full. A number
// that keeps growing is the signal that the endpoint is slower than dispatch.
func (s *JevShadow) Dropped() int64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// Skipped counts observations the budget or the breaker refused. Unlike
// Dropped (a full queue), these never made a call and never wrote a row, so a
// rising number is the signal that the cap is doing its job.
func (s *JevShadow) Skipped() int64 {
	if s == nil {
		return 0
	}
	var total int64
	if s.budget != nil {
		total += s.budget.skippedCalls()
	}
	if s.breaker != nil {
		total += s.breaker.skippedCalls()
	}
	return total
}

// allowCall applies both gates immediately before a vendor call. The breaker
// is checked first: while it is open no call should consume a budget slot, so
// the cap is still available when the pause ends. Both refusals are counted in
// memory and logged, never written to jev_shadow_decision.
func (s *JevShadow) allowCall(now time.Time) bool {
	if s.breaker != nil && !s.breaker.allow(now) {
		s.log.Debug("jev shadow: circuit open, skipped", "decision_point", "shadow", "skipped_total", s.Skipped())
		return false
	}
	if s.budget != nil && !s.budget.allow(now) {
		s.log.Debug("jev shadow: over hourly budget, skipped", "max_per_hour", s.budget.max, "skipped_total", s.Skipped())
		return false
	}
	return true
}

// Snapshot reads the card's state for one dispatch. It is called before the
// dispatch so the answer predicts the run; when shadow mode is off it returns
// immediately and reads nothing.
func (s *JevShadow) Snapshot(ctx context.Context, w db.IssueWakeup) *WakeupDispatchSnapshot {
	if !s.Enabled() || s.db == nil || !w.ID.Valid {
		return nil
	}
	snap := &WakeupDispatchSnapshot{
		WorkspaceID: util.UUIDToString(w.WorkspaceID),
		WakeupID:    util.UUIDToString(w.ID),
		IssueID:     util.UUIDToString(w.IssueID),
		Kind:        w.Kind,
		Mode:        w.Mode,
		SystemRule:  w.SystemRule.String,
		IsSystem:    w.SystemRule.Valid,
	}
	// The condition's type only: the predicate itself can carry a pull request,
	// a comment or a title, and none of that leaves this process.
	if len(w.Condition) > 0 {
		var condition struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(w.Condition, &condition) == nil {
			snap.ConditionType = condition.Type
		}
	}
	if snap.IssueID == "" {
		return snap
	}

	// One row carries the whole issue half of the state: a dispatch that reads
	// four times to describe one card would cost more than the observation is
	// worth.
	var updatedAt time.Time
	err := s.db.QueryRow(ctx, `
		SELECT i.status, COALESCE(i.assignee_type, ''), COALESCE(i.assignee_id::text, ''), i.updated_at,
		       EXISTS (SELECT 1 FROM agent_task_queue q
		                WHERE q.issue_id = i.id
		                  AND q.status IN ('queued','dispatched','running','waiting_local_directory')),
		       (SELECT count(*) FROM issue_wakeup w2
		         WHERE w2.issue_id = i.id AND w2.enabled AND w2.disabled_at IS NULL
		           AND w2.id <> $2::uuid)
		  FROM issue i WHERE i.id = $1::uuid`,
		snap.IssueID, snap.WakeupID,
	).Scan(&snap.IssueStatus, &snap.AssigneeType, &snap.AssigneeID, &updatedAt, &snap.HasLiveRun, &snap.OtherLiveWakeups)
	if err != nil {
		// A snapshot that cannot be read is simply not observed: the dispatch
		// is what matters, and it is about to happen either way.
		s.log.Debug("jev shadow: snapshot unavailable", "wakeup_id", snap.WakeupID, "error", s.client.Redact(err.Error()))
		return nil
	}
	if idle := s.now().Sub(updatedAt); idle > 0 {
		snap.IdleMinutes = int(idle.Minutes())
	}
	return snap
}

// Assess queues one observation for the dispatch that just happened. It never
// blocks: a full queue drops the observation and counts it.
func (s *JevShadow) Assess(snap *WakeupDispatchSnapshot) {
	if !s.Enabled() || snap == nil {
		return
	}
	s.enqueue(jevShadowJob{
		state:         snap.State(),
		workspaceID:   snap.WorkspaceID,
		subjectID:     snap.WakeupID,
		issueID:       snap.IssueID,
		decisionPoint: jevShadowDecisionPointWakeup,
		subjectType:   jevShadowSubjectType,
		questionKey:   jevShadowQuestionKey,
		question: jev.Noul(
			"This wakeup dispatch is about to start an agent run for the card described in the state. "+
				"Will that run do something on the card, or will it check in and find that nothing needs action?",
			"the run will take at least one real action on the card (a comment, a status move, a wakeup, an assignment)",
			"the run will find nothing to do and check in without acting",
		),
		verdictFor: func(a jev.Answer) string {
			if a.Noul >= jevShadowThreshold {
				return "act"
			}
			return "no_action"
		},
		threshold: new(jevShadowThreshold),
		logKey:    "wakeup_id",
	})
}

// enqueue hands one observation to the worker pool. It never blocks: a full
// queue drops the observation and counts it.
func (s *JevShadow) enqueue(job jevShadowJob) {
	if !s.Enabled() {
		return
	}
	s.inFlight.Add(1)
	select {
	case s.jobs <- job:
	default:
		s.inFlight.Done()
		s.dropped.Add(1)
		s.log.Debug("jev shadow: dropped", job.logKey, job.subjectID, "dropped_total", s.dropped.Load())
	}
}

// Wait blocks until every queued observation has been answered or dropped. The
// product never calls it; a test does, so an assertion about a row is not a
// race.
func (s *JevShadow) Wait() {
	if s == nil {
		return
	}
	s.inFlight.Wait()
}

func (s *JevShadow) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.jobs:
			s.observe(ctx, job)
			s.inFlight.Done()
		}
	}
}

// observe asks one question and writes one row. Every failure stays here: the
// row records the error kind, and the dispatch it observed is long since done.
// A refusal by the budget or breaker makes no call and writes no row at all —
// otherwise the safety net would put the very noise it stops back in the table.
func (s *JevShadow) observe(ctx context.Context, job jevShadowJob) {
	if !s.allowCall(s.now()) {
		return
	}
	started := s.now()
	result, err := s.client.Decide(ctx, job.state, map[string]jev.Question{job.questionKey: job.question})
	latency := int(s.now().Sub(started).Milliseconds())

	row := jevShadowRow{
		workspaceID:   job.workspaceID,
		decisionPoint: job.decisionPoint,
		subjectType:   job.subjectType,
		subjectID:     job.subjectID,
		issueID:       job.issueID,
		questionKey:   job.questionKey,
		threshold:     job.threshold,
		latencyMS:     latency,
	}
	if err != nil {
		if jev.IsRateLimited(err) {
			// A 429 means the quota is spent, not that the endpoint is broken.
			// Record it apart from faults and open the breaker so the rest of
			// this window costs neither a call nor a row.
			row.errorKind = jevShadowRateLimitedKind
			if s.breaker != nil {
				s.breaker.trip(s.now(), jev.RetryAfterOf(err))
			}
		} else {
			row.errorKind = string(jev.KindOf(err))
			if row.errorKind == "" {
				row.errorKind = "unknown"
			}
		}
		s.log.Debug("jev shadow: no answer", job.logKey, job.subjectID, "kind", row.errorKind, "error", s.client.Redact(err.Error()))
	} else {
		answer := result.Answers[job.questionKey]
		row.model = result.Model
		row.answer = mustJSON(answer)
		row.usage = mustJSON(nonNilMap(result.Usage))
		if job.verdictFor != nil {
			row.verdict = job.verdictFor(answer)
		}
	}
	if err := s.insert(ctx, row); err != nil {
		s.log.Debug("jev shadow: row not written", job.logKey, job.subjectID, "error", s.client.Redact(err.Error()))
	}
}

// jevShadowRow is one row of jev_shadow_decision, as observe fills it.
// Threshold is a pointer because choice rows carry no split: NULL, not 0.
type jevShadowRow struct {
	workspaceID   string
	decisionPoint string
	subjectType   string
	subjectID     string
	issueID       string
	questionKey   string
	answer        string
	threshold     *float64
	verdict       string
	model         string
	usage         string
	latencyMS     int
	errorKind     string
}

// insert writes the observation. The SQL is hand-written rather than generated:
// sqlc is not part of this build, and this table has exactly one writer and one
// reader (a later join), so a query in the query files would be a second place
// to keep true for no gain.
func (s *JevShadow) insert(ctx context.Context, row jevShadowRow) error {
	if s.db == nil {
		return nil
	}
	var threshold any
	if row.threshold != nil {
		threshold = *row.threshold
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := s.db.Exec(writeCtx, `
		INSERT INTO jev_shadow_decision
		    (workspace_id, decision_point, subject_type, subject_id, issue_id, question_key,
		     answer, threshold, verdict, model, usage, latency_ms, error_kind)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6,
		        NULLIF($7, '')::jsonb, $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, '')::jsonb, $12, NULLIF($13, ''))`,
		row.workspaceID, row.decisionPoint, row.subjectType, row.subjectID, row.issueID, row.questionKey,
		row.answer, threshold, row.verdict, row.model, row.usage, row.latencyMS, row.errorKind,
	)
	return err
}

// mustJSON encodes a value for a jsonb column, or "" when it cannot be encoded,
// which the insert turns into NULL.
func mustJSON(value any) string {
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// ReclaimSnapshot is the structured state sent for one reclaimed task,
// captured AFTER the reclaim UPDATE committed, from the returned row. The
// UPDATE identifies the task — no row exists to snapshot before it — and it
// resets dispatched_at and the prepare lease, so the state uses the returned
// values (which columns reset: dispatched_at is refreshed to now() and
// prepare_lease_expires_at is extended by prepare_lease_duration on every
// recovery) rather than anything read earlier.
type ReclaimSnapshot struct {
	WorkspaceID string
	TaskID      string
	IssueID     string
	AgentID     string
	RuntimeID   string
	Priority    int32
	Attempt     int32
	// DispatchedAgeSeconds is how long the task sat dispatched before this
	// recovery re-delivered it.
	DispatchedAgeSeconds int64
	IssueStatus          string
	AssigneeType         string
	IdleMinutes          int
	HasLiveRun           bool
}

// State is the JSON body sent as the call's state: structured metadata about
// the reclaimed task and its card, and nothing a person wrote.
func (s *ReclaimSnapshot) State() map[string]any {
	return map[string]any{
		"decision": jevShadowDecisionPointReclaim,
		"task": map[string]any{
			"priority":               s.Priority,
			"attempt":                s.Attempt,
			"dispatched_age_seconds": s.DispatchedAgeSeconds,
		},
		"issue": map[string]any{
			"status":        s.IssueStatus,
			"assignee_type": s.AssigneeType,
			"idle_minutes":  s.IdleMinutes,
			"has_live_run":  s.HasLiveRun,
		},
	}
}

// SnapshotReclaim builds the state for one reclaimed task from the UPDATE's
// returned row plus the one-row issue read. It is called after the reclaim
// committed, so the answer predicts whether the re-delivered task will start
// and act; when shadow mode is off it returns immediately and reads nothing.
// Observe stays asynchronous through the pool: this function never calls the
// vendor, and the enqueue never runs inside the claim path's transaction.
func (s *JevShadow) SnapshotReclaim(task db.AgentTaskQueue) *ReclaimSnapshot {
	if !s.Enabled() || s.db == nil || !task.ID.Valid {
		return nil
	}
	var dispatchedAt time.Time
	if task.DispatchedAt.Valid {
		dispatchedAt = task.DispatchedAt.Time
	}
	snap := &ReclaimSnapshot{
		TaskID:    util.UUIDToString(task.ID),
		IssueID:   util.UUIDToString(task.IssueID),
		AgentID:   util.UUIDToString(task.AgentID),
		RuntimeID: util.UUIDToString(task.RuntimeID),
		Priority:  task.Priority,
		Attempt:   task.Attempt,
	}
	if age := s.now().Sub(dispatchedAt); age > 0 {
		snap.DispatchedAgeSeconds = int64(age.Seconds())
	}
	if snap.IssueID == "" {
		return nil
	}
	var updatedAt time.Time
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.db.QueryRow(ctx, `
		SELECT i.workspace_id::text, i.status, COALESCE(i.assignee_type, ''), i.updated_at,
		       EXISTS (SELECT 1 FROM agent_task_queue q
		                WHERE q.issue_id = i.id
		                  AND q.status IN ('queued','dispatched','running','waiting_local_directory')
		                  AND q.id <> $2::uuid)
		  FROM issue i WHERE i.id = $1::uuid`,
		snap.IssueID, snap.TaskID,
	).Scan(&snap.WorkspaceID, &snap.IssueStatus, &snap.AssigneeType, &updatedAt, &snap.HasLiveRun)
	if err != nil || snap.WorkspaceID == "" {
		s.log.Debug("jev shadow: reclaim snapshot unavailable", "task_id", snap.TaskID, "error", s.client.Redact(err.Error()))
		return nil
	}
	if idle := s.now().Sub(updatedAt); idle > 0 {
		snap.IdleMinutes = int(idle.Minutes())
	}
	return snap
}

// AssessReclaim queues one observation for a task the claim path just
// re-delivered. It never blocks and never runs inside the claim transaction:
// a full queue drops the observation and counts it.
func (s *JevShadow) AssessReclaim(snap *ReclaimSnapshot) {
	if !s.Enabled() || snap == nil {
		return
	}
	s.enqueue(jevShadowJob{
		state:         snap.State(),
		workspaceID:   snap.WorkspaceID,
		subjectID:     snap.TaskID,
		issueID:       snap.IssueID,
		decisionPoint: jevShadowDecisionPointReclaim,
		subjectType:   jevShadowSubjectTypeTask,
		questionKey:   jevShadowQuestionKeyResume,
		question: jev.Noul(
			"This stale dispatched task was just re-delivered to its runtime after its first dispatch never started. "+
				"Will the re-delivered task start and do work, or will it idle or fail again?",
			"the re-delivered task will start and take at least one real action",
			"the re-delivered task will idle, expire or fail without acting",
		),
		verdictFor: func(a jev.Answer) string {
			if a.Noul >= jevShadowThreshold {
				return "act"
			}
			return "no_action"
		},
		threshold: new(jevShadowThreshold),
		logKey:    "task_id",
	})
}

// Route labels for the triage-routing choice. Unmapped assignees count as
// "other" in the report, never as disagreed — the lookup below and the SQL
// share this set.
var jevRouteLabels = []string{"engineer", "reviewer", "architect", "leader", "owner"}

// jevRouteOptions describes each route for the choice question.
var jevRouteOptions = map[string]string{
	"engineer":  "code, fix or build work on the card by an engineer agent",
	"reviewer":  "review the card's diff or plan without changing code",
	"architect": "design or verdict on the card by an architect agent",
	"leader":    "drive, triage or coordinate the card by the lead agent",
	"owner":     "needs the human owner's decision before any agent acts",
}

// RouteSnapshot is the structured state sent for one assign write: metadata
// about the card and the actor that routed it, and nothing a person wrote.
// No titles, comment bodies or instruction text — the egress boundary holds.
type RouteSnapshot struct {
	WorkspaceID  string
	IssueID      string
	Actor        string
	Status       string
	Priority     string
	Parent       string
	HasPR        bool
	Stage        string
	Labels       []string
	Project      string
	Important    *int32
	Complexity   *int32
	AssigneeType string
	AssigneeID   string
}

// State is the JSON body sent as the call's state: structured metadata about
// the routed card, and nothing a person wrote.
func (s *RouteSnapshot) State() map[string]any {
	state := map[string]any{
		"decision": jevShadowDecisionPointRoute,
		"actor":    s.Actor,
		"issue": map[string]any{
			"status":        s.Status,
			"priority":      s.Priority,
			"parent":        s.Parent,
			"has_pr":        s.HasPR,
			"stage":         s.Stage,
			"labels":        s.Labels,
			"project":       s.Project,
			"assignee_type": s.AssigneeType,
			"assignee_id":   s.AssigneeID,
		},
	}
	if s.Important != nil {
		state["issue"].(map[string]any)["important_level"] = *s.Important
	}
	if s.Complexity != nil {
		state["issue"].(map[string]any)["complexity"] = *s.Complexity
	}
	return state
}

// routeActorIsRouter reports whether the acting agent routes triage: a Leader
// or Orchestrator run acting through its agent identity. Matching is by role
// in the agent name, case-insensitive — the display name is owner-editable,
// but the Leader/Orchestrator/lead seats keep their role in the name, and a
// human assign (actorType "member") never qualifies.
func routeActorIsRouter(actorType string, name string) bool {
	if actorType != "agent" {
		return false
	}
	lower := strings.ToLower(name)
	return strings.Contains(lower, "orchestrator") || strings.Contains(lower, "leader") ||
		strings.Contains(lower, "lead")
}

// SnapshotRoute builds the state for one assign write. Nil unless the write
// is a Leader/Orchestrator reassign to a different assignee: human assigns,
// creates, workflow handoffs and same-assignee writes carry no routing
// decision to shadow.
func (s *JevShadow) SnapshotRoute(ctx context.Context, issue db.Issue, prevAssigneeType, prevAssigneeID string, actorType, actorID string, isCreate, isHandoff bool) *RouteSnapshot {
	if !s.Enabled() || s.db == nil || isCreate || isHandoff {
		return nil
	}
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return nil
	}
	curType, curID := issue.AssigneeType.String, util.UUIDToString(issue.AssigneeID)
	if curType == prevAssigneeType && curID == prevAssigneeID {
		return nil
	}
	if issue.TriageState.Valid {
		return nil
	}
	var actingName string
	q := db.New(s.db)
	if err := s.db.QueryRow(ctx, `SELECT name FROM agent WHERE id = $1::uuid`, actorID).Scan(&actingName); err != nil || !routeActorIsRouter(actorType, actingName) {
		return nil
	}
	snap := &RouteSnapshot{
		WorkspaceID:  util.UUIDToString(issue.WorkspaceID),
		IssueID:      util.UUIDToString(issue.ID),
		Actor:        strings.ToLower(strings.TrimSpace(actingName)),
		Status:       issue.Status,
		Priority:     issue.Priority,
		Parent:       util.UUIDToString(issue.ParentIssueID),
		Project:      util.UUIDToString(issue.ProjectID),
		AssigneeType: curType,
		AssigneeID:   curID,
	}
	if issue.Stage.Valid {
		snap.Stage = strconv.Itoa(int(issue.Stage.Int32))
	}
	if sha, err := q.GetIssueReviewHeadSha(ctx, issue.ID); err == nil && sha != "" {
		snap.HasPR = true
	}
	if labels, err := q.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err == nil {
		for _, l := range labels {
			snap.Labels = append(snap.Labels, l.Name)
		}
	}
	// Difficulty rides along when set; absence is itself a signal (B4: only
	// 0.5% of cards carry these fields). Same deterministic lookup as
	// difficultyFromProperties: active workspace number defs, lowest
	// property-UUID wins on duplicates, JSON-number 1..5 or omitted.
	if important, complexity, ok := routeDifficulty(ctx, s.db, issue); ok {
		snap.Important, snap.Complexity = important, complexity
	}
	return snap
}

// routeDifficulty reads important_level/complexity for one issue. ok is false
// when neither field is set, so the snapshot omits both keys rather than
// recording a default the card never carried.
func routeDifficulty(ctx context.Context, database db.DBTX, issue db.Issue) (important, complexity *int32, ok bool) {
	if len(issue.Properties) == 0 {
		return nil, nil, false
	}
	rows, err := database.Query(ctx, `
		SELECT id::text, lower(name) FROM issue_property
		 WHERE workspace_id = $1::uuid AND archived_at IS NULL AND type = 'number'
		   AND lower(name) IN ('important_level', 'complexity')
		 ORDER BY id ASC`)
	if err != nil {
		return nil, nil, false
	}
	defer rows.Close()
	want := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		if prev, seen := want[name]; !seen || id < prev {
			want[name] = id
		}
	}
	if len(want) == 0 {
		return nil, nil, false
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(issue.Properties, &bag); err != nil {
		return nil, nil, false
	}
	read := func(name string) *int32 {
		id, defined := want[name]
		if !defined {
			return nil
		}
		var f float64
		if err := json.Unmarshal(bag[id], &f); err != nil || f != math.Trunc(f) || f < 1 || f > 5 {
			return nil
		}
		return new(int32(f))
	}
	important, complexity = read("important_level"), read("complexity")
	if important == nil && complexity == nil {
		return nil, nil, false
	}
	return important, complexity, true
}

// AssessRoute queues one observation for an assign write the router just made.
// It never blocks: a full queue drops the observation and counts it.
func (s *JevShadow) AssessRoute(snap *RouteSnapshot) {
	if !s.Enabled() || snap == nil {
		return
	}
	s.enqueue(jevShadowJob{
		state:         snap.State(),
		workspaceID:   snap.WorkspaceID,
		subjectID:     snap.IssueID,
		issueID:       snap.IssueID,
		decisionPoint: jevShadowDecisionPointRoute,
		subjectType:   "issue",
		questionKey:   jevShadowQuestionKeyRoute,
		question: jev.Choice(
			"Which route fits this card: which of engineer, reviewer, architect, leader or owner should own it next?",
			jevRouteOptions,
		),
		verdictFor: func(a jev.Answer) string { return a.Choice },
		threshold:  nil,
		logKey:     "issue_id",
	})
}
