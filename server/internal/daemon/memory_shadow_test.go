package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/initctx"
)

// shadowMoctServer answers the pre-run inject with one fixed record, so two
// runs can be compared byte for byte.
func shadowMoctServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"entries":[{"id":"rec-1","type":"convention","title":"One writer","body":"Keep the seam one writer."}],"tokens":12}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// shadowPrepareServer answers prepare with a ready block and records the body
// this host actually sent, so the request can be compared with the primary
// seam's own.
func shadowPrepareServer(t *testing.T, calls *atomic.Int32, body *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if body != nil {
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			*body = got
		}
		_, _ = w.Write([]byte(`{"status":"ready","content":"### Shadow plane\n\nNever inject me.","content_bytes":38}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMemoryShadowLeavesTheInjectByteIdentical is the acceptance check that the
// dogfood changes nothing an agent sees: with the shadow on, the MOCT block, its
// note and its token count match a shadow-off claim exactly, and the second
// backend's text never appears in it.
func TestMemoryShadowLeavesTheInjectByteIdentical(t *testing.T) {
	moctSrv := shadowMoctServer(t)
	var pcCalls atomic.Int32
	var pcBody map[string]any
	pcSrv := shadowPrepareServer(t, &pcCalls, &pcBody)

	offCfg := Config{MoctBaseURL: moctSrv.URL, PowerContextBaseURL: pcSrv.URL}
	onCfg := offCfg
	onCfg.MemoryShadow = "powercontext"

	offBlock, offPart := memoryInjectPrompt(context.Background(), offCfg, moctTestTask(), moctTestLogger())
	onBlock, onPart := memoryInjectPrompt(context.Background(), onCfg, moctTestTask(), moctTestLogger())

	if offBlock == "" {
		t.Fatal("the fixture injected nothing; the comparison would be vacuous")
	}
	if onBlock != offBlock {
		t.Fatalf("inject changed with the shadow on:\noff %q\non  %q", offBlock, onBlock)
	}
	if onPart.Tokens != offPart.Tokens || onPart.Note != offPart.Note {
		t.Fatalf("memory row changed with the shadow on: %+v vs %+v", offPart, onPart)
	}
	if strings.Contains(onBlock, "Never inject me") {
		t.Fatalf("the shadow block reached the prompt: %q", onBlock)
	}
	if got := pcCalls.Load(); got != 0 {
		t.Fatalf("the claim path called PowerContext %d times; the shadow is a separate read", got)
	}

	shadow, note := newMemoryShadow(onCfg, moctTestTask(), moctTestLogger())
	if shadow == nil || note != "" {
		t.Fatalf("newMemoryShadow = (%v, %q), want a started read and no note", shadow, note)
	}
	shadow.start(context.Background())
	record, ok := shadow.wait(3 * time.Second)
	if !ok {
		t.Fatal("the shadow read did not finish")
	}
	if record.Error != "" {
		t.Fatalf("shadow error = %q", record.Error)
	}
	if !strings.Contains(record.Block, "Never inject me") {
		t.Fatalf("shadow block = %q, want the second plane's text", record.Block)
	}
	if record.Bytes != len(record.Block) || record.Tokens != initctx.Estimate(record.Block) {
		t.Fatalf("shadow pricing = %d bytes / %d tokens for a %d-byte block", record.Bytes, record.Tokens, len(record.Block))
	}
	if record.Backend != memoryShadowPowerContext {
		t.Fatalf("shadow backend = %q", record.Backend)
	}
	// Same trigger means the same request the primary seam would send: the
	// project scope and the memory part's byte budget.
	if pcBody["scope_id"] != "multica:project:"+moctTestProjectID {
		t.Fatalf("scope_id = %v", pcBody["scope_id"])
	}
	if pcBody["max_bytes"] != float64(powerContextMaxBytes()) {
		t.Fatalf("max_bytes = %v, want %d", pcBody["max_bytes"], powerContextMaxBytes())
	}
	if got := pcCalls.Load(); got != 1 {
		t.Fatalf("PowerContext calls = %d, want exactly one shadow read", got)
	}
}

// TestMemoryShadowTimeoutIsRecordedAndNeverFatal is the acceptance check that a
// slow second backend costs the run nothing: the claim's block is unchanged and
// the failure lands on the memory part as a fact.
func TestMemoryShadowTimeoutIsRecordedAndNeverFatal(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"ready","content":"too late","content_bytes":8}`))
	}))
	t.Cleanup(slow.Close)

	previous := memoryShadowTimeout
	memoryShadowTimeout = 40 * time.Millisecond
	t.Cleanup(func() { memoryShadowTimeout = previous })

	moctSrv := shadowMoctServer(t)
	cfg := Config{MoctBaseURL: moctSrv.URL, PowerContextBaseURL: slow.URL, MemoryShadow: "powercontext"}

	block, part := memoryInjectPrompt(context.Background(), cfg, moctTestTask(), moctTestLogger())
	if block == "" || part.Tokens == 0 {
		t.Fatal("the primary block must survive a dead shadow backend")
	}

	shadow, _ := newMemoryShadow(cfg, moctTestTask(), moctTestLogger())
	shadow.start(context.Background())
	record, ok := shadow.wait(3 * time.Second)
	if !ok {
		t.Fatal("the shadow read did not finish; a timeout must end it")
	}
	if !strings.Contains(record.Error, "timed out") {
		t.Fatalf("shadow error = %q, want the timeout named", record.Error)
	}
	if record.Block != "" {
		t.Fatalf("a timed-out read recorded a block: %q", record.Block)
	}
	if record.LatencyMS <= 0 {
		t.Fatalf("latency = %d ms, want the wait priced", record.LatencyMS)
	}
}

// TestMemoryShadowStaysOffByDefault pins the default and every way a lane can
// set the switch and still get no read. Each unusable setting leaves a note,
// because "I turned it on and nothing happened" is the question the run row has
// to answer.
func TestMemoryShadowStaysOffByDefault(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		wantNote string
	}{
		{name: "unset", cfg: Config{MoctBaseURL: "http://127.0.0.1:8110", PowerContextBaseURL: "http://127.0.0.1:8000"}},
		{name: "explicitly off", cfg: Config{MoctBaseURL: "http://127.0.0.1:8110", PowerContextBaseURL: "http://127.0.0.1:8000", MemoryShadow: " off "}, wantNote: ""},
		{name: "no shadow url", cfg: Config{MoctBaseURL: "http://127.0.0.1:8110", MemoryShadow: "powercontext"}, wantNote: "needs POWERCONTEXT_BASE_URL"},
		{name: "moct is not the primary", cfg: Config{PowerContextBaseURL: "http://127.0.0.1:8000", MemoryShadow: "powercontext"}, wantNote: "no MOCT_BASE_URL"},
		{name: "unknown backend", cfg: Config{MoctBaseURL: "http://127.0.0.1:8110", PowerContextBaseURL: "http://127.0.0.1:8000", MemoryShadow: "ollama"}, wantNote: "is not a memory backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shadow, note := newMemoryShadow(tc.cfg, moctTestTask(), moctTestLogger())
			if shadow != nil {
				t.Fatalf("shadow = %+v, want nil", shadow)
			}
			if !strings.Contains(note, tc.wantNote) {
				t.Fatalf("note = %q, want it to contain %q", note, tc.wantNote)
			}
		})
	}
}

// TestApplyMemoryShadowLandsOnTheMemoryPartOnly proves the record reaches the
// run row without being priced into the run: totals are byte-identical before
// and after the shadow is applied.
func TestApplyMemoryShadowLandsOnTheMemoryPartOnly(t *testing.T) {
	var calls atomic.Int32
	pc := shadowPrepareServer(t, &calls, nil)
	cfg := Config{MoctBaseURL: "http://127.0.0.1:8110", PowerContextBaseURL: pc.URL, MemoryShadow: "powercontext"}

	shadow, _ := newMemoryShadow(cfg, moctTestTask(), moctTestLogger())
	shadow.start(context.Background())
	if _, ok := shadow.wait(3 * time.Second); !ok {
		t.Fatal("the shadow read did not finish")
	}

	report := initctx.Finalize("claude", []initctx.PartReport{
		{Part: initctx.PartMemory, Tokens: 100, Measured: true},
		{Part: initctx.PartTask, Tokens: 50, Measured: true},
	}, "sha")
	totalBefore := report.Total

	applyMemoryShadow(report, shadow)

	var memory *initctx.PartReport
	for i := range report.Parts {
		if report.Parts[i].Part == initctx.PartMemory {
			memory = &report.Parts[i]
		}
	}
	if memory == nil || memory.Shadow == nil {
		t.Fatal("the memory part carries no shadow record")
	}
	if !strings.Contains(memory.Shadow.Block, "Never inject me") || memory.Shadow.Tokens <= 0 {
		t.Fatalf("shadow record = %+v", memory.Shadow)
	}
	if report.Total != totalBefore {
		t.Fatalf("total = %d, want %d: the shadow is never priced into the run", report.Total, totalBefore)
	}
}

// TestApplyMemoryShadowIgnoresAnOffSwitch keeps a shadow-less run's row exactly
// as it was: no empty object claiming a comparison happened.
func TestApplyMemoryShadowIgnoresAnOffSwitch(t *testing.T) {
	shadow, note := newMemoryShadow(Config{MoctBaseURL: "http://127.0.0.1:8110"}, moctTestTask(), moctTestLogger())
	if shadow != nil || note != "" {
		t.Fatalf("newMemoryShadow = (%v, %q), want off with no note", shadow, note)
	}
	report := initctx.Finalize("claude", []initctx.PartReport{{Part: initctx.PartMemory, Measured: true}}, "sha")
	applyMemoryShadow(report, shadow)
	for _, part := range report.Parts {
		if part.Shadow != nil {
			t.Fatalf("part %s carries a shadow record with the switch off", part.Part)
		}
	}
}

// TestMemoryShadowUnfinishedReadIsRecorded covers a run that ends before the
// read does: the row says so rather than blocking the run or pretending the
// second plane was empty.
func TestMemoryShadowUnfinishedReadIsRecorded(t *testing.T) {
	shadow, _ := newMemoryShadow(Config{
		MoctBaseURL:         "http://127.0.0.1:8110",
		PowerContextBaseURL: "http://127.0.0.1:1",
		MemoryShadow:        "powercontext",
	}, moctTestTask(), moctTestLogger())
	if shadow == nil {
		t.Fatal("expected a shadow")
	}
	// Never started, which is the state a run is in while the read is in flight.
	record, ok := shadow.snapshot()
	if !ok {
		t.Fatal("snapshot returned nothing")
	}
	if !strings.Contains(record.Note, "had not answered") {
		t.Fatalf("note = %q", record.Note)
	}
}
