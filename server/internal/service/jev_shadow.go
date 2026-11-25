package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/jev"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
	// jevShadowDecisionPoint names the fork this shadow observes.
	jevShadowDecisionPoint = "issue_wakeup_dispatch"
	// jevShadowSubjectType is what the question is about: one wakeup rule.
	jevShadowSubjectType = "issue_wakeup"
	// jevShadowQuestionKey is the id the answer comes back under.
	jevShadowQuestionKey = "needs_action"
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
}

type jevShadowJob struct {
	state       map[string]any
	workspaceID string
	wakeupID    string
	issueID     string
}

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
	return newJevShadow(ctx, database, client, log)
}

// newJevShadow starts the worker pool that answers observations. Callers hand it
// a context that is cancelled at shutdown, which is what stops the pool.
func newJevShadow(ctx context.Context, database db.DBTX, client *jev.Client, log *slog.Logger) *JevShadow {
	shadow := &JevShadow{
		client: client,
		db:     database,
		log:    log,
		jobs:   make(chan jevShadowJob, jevShadowQueue),
		now:    time.Now,
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
	job := jevShadowJob{
		state:       snap.State(),
		workspaceID: snap.WorkspaceID,
		wakeupID:    snap.WakeupID,
		issueID:     snap.IssueID,
	}
	s.inFlight.Add(1)
	select {
	case s.jobs <- job:
	default:
		s.inFlight.Done()
		s.dropped.Add(1)
		s.log.Debug("jev shadow: dropped", "wakeup_id", job.wakeupID, "dropped_total", s.dropped.Load())
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
func (s *JevShadow) observe(ctx context.Context, job jevShadowJob) {
	started := s.now()
	result, err := s.client.Decide(ctx, job.state, map[string]jev.Question{
		jevShadowQuestionKey: jev.Noul(
			"This wakeup dispatch is about to start an agent run for the card described in the state. "+
				"Will that run do something on the card, or will it check in and find that nothing needs action?",
			"the run will take at least one real action on the card (a comment, a status move, a wakeup, an assignment)",
			"the run will find nothing to do and check in without acting",
		),
	})
	latency := int(s.now().Sub(started).Milliseconds())

	row := jevShadowRow{
		workspaceID: job.workspaceID,
		wakeupID:    job.wakeupID,
		issueID:     job.issueID,
		threshold:   jevShadowThreshold,
		latencyMS:   latency,
	}
	if err != nil {
		row.errorKind = string(jev.KindOf(err))
		if row.errorKind == "" {
			row.errorKind = "unknown"
		}
		s.log.Debug("jev shadow: no answer", "wakeup_id", job.wakeupID, "kind", row.errorKind, "error", s.client.Redact(err.Error()))
	} else {
		answer := result.Answers[jevShadowQuestionKey]
		row.model = result.Model
		row.answer = mustJSON(answer)
		row.usage = mustJSON(nonNilMap(result.Usage))
		row.verdict = "no_action"
		if answer.Noul >= jevShadowThreshold {
			row.verdict = "act"
		}
	}
	if err := s.insert(ctx, row); err != nil {
		s.log.Debug("jev shadow: row not written", "wakeup_id", job.wakeupID, "error", s.client.Redact(err.Error()))
	}
}

// jevShadowRow is one row of jev_shadow_decision, as observe fills it.
type jevShadowRow struct {
	workspaceID string
	wakeupID    string
	issueID     string
	answer      string
	threshold   float64
	verdict     string
	model       string
	usage       string
	latencyMS   int
	errorKind   string
}

// insert writes the observation. The SQL is hand-written rather than generated:
// sqlc is not part of this build, and this table has exactly one writer and one
// reader (a later join), so a query in the query files would be a second place
// to keep true for no gain.
func (s *JevShadow) insert(ctx context.Context, row jevShadowRow) error {
	if s.db == nil {
		return nil
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := s.db.Exec(writeCtx, `
		INSERT INTO jev_shadow_decision
		    (workspace_id, decision_point, subject_type, subject_id, issue_id, question_key,
		     answer, threshold, verdict, model, usage, latency_ms, error_kind)
		VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6,
		        NULLIF($7, '')::jsonb, $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, '')::jsonb, $12, NULLIF($13, ''))`,
		row.workspaceID, jevShadowDecisionPoint, jevShadowSubjectType, row.wakeupID, row.issueID, jevShadowQuestionKey,
		row.answer, row.threshold, row.verdict, row.model, row.usage, row.latencyMS, row.errorKind,
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
