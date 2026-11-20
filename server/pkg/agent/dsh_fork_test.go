package agent

// S3-B2 (M-138): the seeded start is gated on the bridge's `fork` capability.
// These tests pin both halves of that gate at the wire level: a fork-capable
// bridge receives fork_parent + seed and reports the fork as used, while a
// bridge that does not advertise it receives the plain resume instead — never
// the fields it cannot parse.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// dshForkFixture captures the execute frame the daemon wrote, then plays a
// normal run to completion. Capturing beats asserting inside the shell: a
// mismatch in the fixture exits before the result frame, and the failure reads
// as an opaque send error instead of showing what was actually on the wire.
func dshForkFixture(t *testing.T, ready string) (bin string, framePath string) {
	t.Helper()
	bin = writeDshFixture(t, `
printf '%s\n' '`+ready+`'
IFS= read -r command
printf '%s\n' "$command" > execute-frame.json
printf '%s\n' '{"v":1,"type":"session","request_id":"task-fork","session_id":"session-child","resumed":false}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-fork","status":"completed","session_id":"session-child","output":"done","resume_rejected":false}'
`)
	return bin, "execute-frame.json"
}

func readDshExecuteFrame(t *testing.T, cwd, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cwd, name))
	if err != nil {
		t.Fatalf("read captured execute frame: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("execute frame is not JSON: %v\n%s", err, raw)
	}
	return frame
}

func TestDshForkSeedReachesAForkCapableBridge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin, capture := dshForkFixture(t,
		`{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{"fork":true}}`)
	cwd := t.TempDir()
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-fork", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "carry on", ExecOptions{
		Cwd:        cwd,
		Timeout:    10 * time.Second,
		ForkParent: "session-parent",
		Seed:       "Summary of the parent session.\n\nNow do this.",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("bad result: %#v", result)
	}
	if !result.DshForkUsed {
		t.Fatal("DshForkUsed false for a bridge that advertised fork")
	}
	frame := readDshExecuteFrame(t, cwd, capture)
	if got, _ := frame["fork_parent"].(string); got != "session-parent" {
		t.Fatalf("fork_parent = %q, want session-parent (frame: %v)", got, frame)
	}
	if got, _ := frame["seed"].(string); got != "Summary of the parent session.\n\nNow do this." {
		t.Fatalf("seed = %q", got)
	}
	// A fork starts a NEW session; asking the bridge to resume the parent as
	// well would fork from a conversation it was told to continue.
	if got, present := frame["resume_session_id"]; present {
		t.Fatalf("resume_session_id = %v on a fork frame, want it absent", got)
	}
}

func TestDshForkFallsBackToResumeWithoutTheCapability(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	// An older bridge: no `fork` on the ready frame. It never sees fork_parent
	// or seed; the run continues the parent session the way every pre-S3-B2
	// run did, and the daemon reports that no fork happened so the run row can
	// say "resumed warm" rather than claim a seed it never sent.
	bin, capture := dshForkFixture(t,
		`{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{"questions":true}}`)
	cwd := t.TempDir()
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-fork", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "carry on", ExecOptions{
		Cwd:        cwd,
		Timeout:    10 * time.Second,
		ForkParent: "session-parent",
		Seed:       "Summary of the parent session.",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("bad result: %#v", result)
	}
	if result.DshForkUsed {
		t.Fatal("DshForkUsed true for a bridge that never advertised fork")
	}
	frame := readDshExecuteFrame(t, cwd, capture)
	if got, _ := frame["resume_session_id"].(string); got != "session-parent" {
		t.Fatalf("resume_session_id = %q, want session-parent (frame: %v)", got, frame)
	}
	for _, field := range []string{"fork_parent", "seed"} {
		if value, present := frame[field]; present {
			t.Fatalf("%s = %v sent to a bridge without the fork capability", field, value)
		}
	}
}

// The first session frame after a fork request settles DshForkUsed (S3-fix A,
// M-180, F18): only a new id that is not the parent's is the forked child. The
// bridge's seed validation can reject the seed and resume the parent instead,
// and the run row must follow that report: "resumed warm", never "seeded".
func TestDshSessionFrameSettlesTheForkClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bool
		sessionID string
		resumed   bool
		want      bool
	}{
		{name: "bridge forked a child", requested: true, sessionID: "session-child", want: true},
		{name: "bridge resumed the parent", requested: true, sessionID: "session-parent", resumed: true, want: false},
		{name: "parent id without resumed flag", requested: true, sessionID: "session-parent", want: false},
		{name: "no fork requested", sessionID: "session-child", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &dshRunState{requestID: "task-fork", forkParent: "session-parent"}
			state.forkRequested.Store(tc.requested)
			ch := make(chan Message, 4)
			frame := dshFrame{
				Version:   DshProtocolVersion,
				Type:      "session",
				RequestID: "task-fork",
				SessionID: tc.sessionID,
				Resumed:   tc.resumed,
			}
			handleDshFrame(frame, "task-fork", ch, state)
			if got := state.forkUsed.Load(); got != tc.want {
				t.Fatalf("forkUsed = %v, want %v", got, tc.want)
			}
			// Only the first session frame settles the claim.
			frame.SessionID, frame.Resumed = "session-other", false
			handleDshFrame(frame, "task-fork", ch, state)
			if got := state.forkUsed.Load(); got != tc.want {
				t.Fatalf("forkUsed changed on a later session frame: %v, want %v", got, tc.want)
			}
		})
	}
}

// A fork the bridge fails before reporting any session must not claim a seed:
// the fork command went out, but no forked child ever existed.
func TestDshForkFailingBeforeSessionFrameClaimsNoFork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{"fork":true}}'
IFS= read -r command
printf '%s\n' "$command" | grep -q '"fork_parent":"session-parent"' || exit 7
printf '%s\n' '{"v":1,"type":"result","request_id":"task-fork","status":"failed","output":"","resume_rejected":false,"error":{"code":"DSH_EXECUTION_FAILED","message":"mcp startup failed"}}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-fork", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "carry on", ExecOptions{
		Cwd: t.TempDir(), Timeout: 10 * time.Second,
		ForkParent: "session-parent", Seed: "summary of the parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "failed" {
		t.Fatalf("fixture should report the failed fork (exit 7 means fork_parent never went out): %#v", result)
	}
	if result.DshForkUsed {
		t.Fatal("DshForkUsed true for a fork that failed before any session frame")
	}
}

// The detached path (S3-C) writes its execute frame before any ready frame can
// arrive, so there is no capability to gate on. It must degrade to the plain
// resume rather than send a fork the bridge may not understand.
func TestDshForkDegradesToResumeOnDetachedRuns(t *testing.T) {
	command := dshExecuteCommand{
		Version:    DshProtocolVersion,
		Type:       "execute",
		RequestID:  "task-detached",
		Cwd:        "/work",
		Prompt:     "carry on",
		ForkParent: "session-parent",
		Seed:       "summary",
	}
	dshDegradeForkToResume(&command)

	encoded, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"fork_parent", "seed"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("%s survived the detached fallback: %s", field, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"resume_session_id":"session-parent"`) {
		t.Fatalf("detached fallback lost the resume target: %s", encoded)
	}
}
