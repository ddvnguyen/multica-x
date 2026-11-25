package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/jev"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeDB is the smallest DBTX the shadow and the dispatch bookkeeping touch: one
// QueryRow for the state snapshot and Exec for everything else. It records what
// was written so a test can assert on the row rather than on a log line.
type fakeDB struct {
	mu sync.Mutex

	status       string
	assigneeType string
	assigneeID   string
	updatedAt    time.Time
	hasLiveRun   bool
	otherWakeups int
	snapshotErr  error

	execs []fakeExec
}

type fakeExec struct {
	sql  string
	args []any
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, fakeExec{sql: sql, args: args})
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("fakeDB: Query is not used by these paths")
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	return fakeRow{db: f}
}

type fakeRow struct{ db *fakeDB }

func (r fakeRow) Scan(dest ...any) error {
	if r.db.snapshotErr != nil {
		return r.db.snapshotErr
	}
	if len(dest) != 6 {
		return errors.New("fakeRow: unexpected column count")
	}
	*(dest[0].(*string)) = r.db.status
	*(dest[1].(*string)) = r.db.assigneeType
	*(dest[2].(*string)) = r.db.assigneeID
	*(dest[3].(*time.Time)) = r.db.updatedAt
	*(dest[4].(*bool)) = r.db.hasLiveRun
	*(dest[5].(*int)) = r.db.otherWakeups
	return nil
}

func (f *fakeDB) shadowRows() []fakeExec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []fakeExec
	for _, exec := range f.execs {
		if strings.Contains(exec.sql, "INSERT INTO jev_shadow_decision") {
			rows = append(rows, exec)
		}
	}
	return rows
}

func discardedLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// callCounter counts what reached the stub endpoint.
type callCounter struct{ hits atomic.Int32 }

func (c *callCounter) Hits() int { return int(c.hits.Load()) }

// countingJevServer answers every call with the given status and body, and
// counts the calls, so "no network change" is an assertion rather than a hope.
func countingJevServer(t *testing.T, status int, body string) (*httptest.Server, *callCounter) {
	t.Helper()
	counter := &callCounter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.hits.Add(1)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, counter
}

func noulBody(noul float64) string {
	encoded, _ := json.Marshal(map[string]any{
		"model":   jev.DefaultModel,
		"answers": map[string]any{"needs_action": map[string]any{"type": "noul", "noul": noul}},
		"usage":   map[string]any{"input_tokens": 231, "output_tokens": 9},
	})
	return string(encoded)
}

func shadowFor(t *testing.T, ctx context.Context, database db.DBTX, base string) *JevShadow {
	t.Helper()
	client, err := jev.NewClient(jev.Config{APIBase: base, APIKey: "sk-test", Timeout: 400 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return newJevShadow(ctx, database, client, discardedLogger())
}

func testWakeup() db.IssueWakeup {
	return db.IssueWakeup{
		ID:          jevTestUUID("11111111-1111-4111-8111-111111111111"),
		WorkspaceID: jevTestUUID("22222222-2222-4222-8222-222222222222"),
		IssueID:     jevTestUUID("33333333-3333-4333-8333-333333333333"),
		Kind:        "event",
		Mode:        "once",
		Enabled:     true,
		Condition:   json.RawMessage(`{"type":"issue_field","field":"status","value":"done","title":"a private card title","instruction_hint":"do the thing"}`),
	}
}

func jevTestUUID(text string) pgtype.UUID {
	var uuid pgtype.UUID
	if err := uuid.Scan(text); err != nil {
		panic(err)
	}
	return uuid
}

// TestStartJevShadowDefaultsToOff is condition 6: no flag means no client, no
// call, and the dispatch path is exactly the one that existed before.
func TestStartJevShadowDefaultsToOff(t *testing.T) {
	srv, hits := countingJevServer(t, http.StatusOK, noulBody(0.9))
	t.Setenv(jevShadowFlag, "")
	t.Setenv("JEV_API_KEY", "sk-test")
	t.Setenv("JEV_API_BASE", srv.URL)

	shadow := StartJevShadow(context.Background(), &fakeDB{}, discardedLogger())
	if shadow != nil {
		t.Fatalf("shadow must be nil with the flag off, got %#v", shadow)
	}
	if shadow.Enabled() {
		t.Error("a nil shadow must report disabled")
	}
	shadow.Assess(nil)
	shadow.Wait()
	if hits.Hits() != 0 {
		t.Errorf("flag off made %d HTTP calls, want 0", hits.Hits())
	}
}

// A key that does not resolve is the second switch: on by flag, off in fact.
func TestStartJevShadowWithoutKeyMakesNoCall(t *testing.T) {
	srv, hits := countingJevServer(t, http.StatusOK, noulBody(0.9))
	t.Setenv(jevShadowFlag, "wakeup")
	t.Setenv("JEV_API_KEY", "")
	t.Setenv(jev.DefaultAPIKeyEnv, "")
	t.Setenv("JEV_API_BASE", srv.URL)

	if shadow := StartJevShadow(context.Background(), &fakeDB{}, discardedLogger()); shadow != nil {
		t.Fatal("shadow must be nil without a key")
	}
	if hits.Hits() != 0 {
		t.Errorf("made %d HTTP calls without a key, want 0", hits.Hits())
	}
}

func TestStartJevShadowEnablesWithFlagAndKey(t *testing.T) {
	srv, _ := countingJevServer(t, http.StatusOK, noulBody(0.9))
	t.Setenv(jevShadowFlag, "wakeup")
	t.Setenv(jev.DefaultAPIKeyEnv, "sk-test")
	t.Setenv("JEV_API_BASE", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if shadow := StartJevShadow(ctx, &fakeDB{}, discardedLogger()); shadow == nil {
		t.Fatal("want an enabled shadow with the flag and a key")
	}
}

// The row is the point: answer, reading of it, model that answered, and cost.
func TestShadowWritesTheAnswer(t *testing.T) {
	srv, hits := countingJevServer(t, http.StatusOK, noulBody(0.9))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database := &fakeDB{
		status:       "in_progress",
		assigneeType: "agent",
		assigneeID:   "44444444-4444-4444-8444-444444444444",
		updatedAt:    time.Now().Add(-90 * time.Minute),
		hasLiveRun:   false,
		otherWakeups: 2,
	}
	shadow := shadowFor(t, ctx, database, srv.URL)

	wakeup := testWakeup()
	snapshot := shadow.Snapshot(ctx, wakeup)
	if snapshot == nil {
		t.Fatal("want a snapshot when the shadow is enabled")
	}
	if snapshot.ConditionType != "issue_field" {
		t.Errorf("condition type = %q, want issue_field", snapshot.ConditionType)
	}
	if snapshot.IdleMinutes < 89 || snapshot.IdleMinutes > 91 {
		t.Errorf("idle minutes = %d, want about 90", snapshot.IdleMinutes)
	}
	shadow.Assess(snapshot)
	shadow.Wait()

	if hits.Hits() != 1 {
		t.Fatalf("HTTP calls = %d, want 1", hits.Hits())
	}
	rows := database.shadowRows()
	if len(rows) != 1 {
		t.Fatalf("shadow rows = %d, want 1", len(rows))
	}
	args := rows[0].args
	if len(args) != 13 {
		t.Fatalf("insert args = %d, want 13", len(args))
	}
	if args[2] != jevShadowSubjectType || args[1] != jevShadowDecisionPoint {
		t.Errorf("row identifies %v/%v, want %s/%s", args[1], args[2], jevShadowDecisionPoint, jevShadowSubjectType)
	}
	if verdict := args[8]; verdict != "act" {
		t.Errorf("verdict = %v, want act", verdict)
	}
	if model := args[9]; model != jev.DefaultModel {
		t.Errorf("model = %v, want %s", model, jev.DefaultModel)
	}
	if answer, _ := args[6].(string); !strings.Contains(answer, "0.9") {
		t.Errorf("answer jsonb = %q, want the vendor's 0.9", answer)
	}
	if usage, _ := args[10].(string); !strings.Contains(usage, "input_tokens") {
		t.Errorf("usage jsonb = %q, want the vendor's usage", usage)
	}
	if kind := args[12]; kind != "" {
		t.Errorf("error_kind = %v, want none on success", kind)
	}
}

// A failed call falls back to "no answer": the row records the kind, and
// nothing above it learns that a shadow existed.
func TestShadowFailureWritesKindAndNoVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		timeout bool
		want    string
	}{
		{name: "http 500", status: http.StatusInternalServerError, body: `{"error":"boom"}`, want: "http"},
		{name: "garbage", status: http.StatusOK, body: `<html>`, want: "protocol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := countingJevServer(t, tc.status, tc.body)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			database := &fakeDB{status: "in_progress", updatedAt: time.Now()}
			shadow := shadowFor(t, ctx, database, srv.URL)

			shadow.Assess(shadow.Snapshot(ctx, testWakeup()))
			shadow.Wait()

			rows := database.shadowRows()
			if len(rows) != 1 {
				t.Fatalf("shadow rows = %d, want 1", len(rows))
			}
			if kind := rows[0].args[12]; kind != tc.want {
				t.Errorf("error_kind = %v, want %s", kind, tc.want)
			}
			if verdict := rows[0].args[8]; verdict != "" {
				t.Errorf("verdict = %v, want none on failure", verdict)
			}
			if answer := rows[0].args[6]; answer != "" {
				t.Errorf("answer = %v, want none on failure", answer)
			}
		})
	}
}

// Condition 5, the important one: the dispatch's returned errors and the fact
// that it ran are identical with the shadow off and with a shadow whose every
// call fails.
func TestDispatchOutcomeIsIdenticalWithShadowOffAndFailing(t *testing.T) {
	dispatchErr := errors.New("dispatch exploded")
	for _, tc := range []struct {
		name       string
		dispatchOK bool
	}{
		{name: "dispatch succeeds", dispatchOK: true},
		{name: "dispatch fails", dispatchOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(shadow *JevShadow) (int, string) {
				database := &fakeDB{status: "in_progress", updatedAt: time.Now()}
				svc := &IssueWakeupService{Tasks: &TaskService{Queries: db.New(database), JevShadow: shadow}}
				calls := 0
				var returned error
				if !tc.dispatchOK {
					returned = dispatchErr
				}
				errs := svc.runDispatch(context.Background(), testWakeup(), func(context.Context, db.IssueWakeup) error {
					calls++
					return returned
				})
				if shadow != nil {
					shadow.Wait()
				}
				return calls, describeErrors(errs)
			}

			failing, _ := countingJevServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			offCalls, offErrs := run(nil)
			onCalls, onErrs := run(shadowFor(t, ctx, &fakeDB{}, failing.URL))

			if offCalls != 1 || onCalls != 1 {
				t.Errorf("dispatch calls off/on = %d/%d, want 1/1", offCalls, onCalls)
			}
			if offErrs != onErrs {
				t.Errorf("returned errors differ\n off: %s\n  on: %s", offErrs, onErrs)
			}
		})
	}
}

func describeErrors(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "|")
}

// A snapshot that cannot be read is not observed, and the dispatch proceeds.
func TestShadowSnapshotFailureIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shadow := shadowFor(t, ctx, &fakeDB{snapshotErr: errors.New("db down")}, "http://127.0.0.1:1")
	if snapshot := shadow.Snapshot(ctx, testWakeup()); snapshot != nil {
		t.Fatalf("want no snapshot, got %#v", snapshot)
	}
	shadow.Assess(nil)
}

// The state that leaves the process is metadata only: the condition's type, not
// its predicate; no instruction, no title. This is the egress boundary the key
// is approved with.
func TestSnapshotStateCarriesNoFreeText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shadow := shadowFor(t, ctx, &fakeDB{status: "in_progress", updatedAt: time.Now()}, "http://127.0.0.1:1")
	snapshot := shadow.Snapshot(ctx, testWakeup())
	if snapshot == nil {
		t.Fatal("want a snapshot")
	}
	encoded, err := json.Marshal(snapshot.State())
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	state := string(encoded)
	for _, forbidden := range []string{"private card title", "do the thing", "instruction_hint", `"value"`, `"field"`} {
		if strings.Contains(state, forbidden) {
			t.Errorf("state carries %q: %s", forbidden, state)
		}
	}
	if !strings.Contains(state, "issue_field") {
		t.Errorf("state lost the condition type: %s", state)
	}
}

// A full queue is a dropped observation, never a waiting dispatch.
func TestShadowDropsWhenTheQueueIsFull(t *testing.T) {
	client, err := jev.NewClient(jev.Config{APIBase: "http://127.0.0.1:1", APIKey: "sk-test", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// No workers: the one queued job stays queued, so the second Assess must
	// drop instead of blocking or growing a goroutine.
	shadow := &JevShadow{client: client, db: &fakeDB{}, log: discardedLogger(), jobs: make(chan jevShadowJob, 1), now: time.Now}
	snapshot := &WakeupDispatchSnapshot{WorkspaceID: "w", WakeupID: "u", IssueID: "i"}

	shadow.Assess(snapshot)
	if dropped := shadow.Dropped(); dropped != 0 {
		t.Fatalf("dropped = %d after one enqueue, want 0", dropped)
	}
	done := make(chan struct{})
	go func() {
		shadow.Assess(snapshot)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Assess blocked on a full queue")
	}
	if dropped := shadow.Dropped(); dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}
