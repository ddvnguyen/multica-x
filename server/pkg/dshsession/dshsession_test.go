package dshsession

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// projectKey vectors are pinned against dsh's format.js projectKey: separator
// runs collapse, safe characters pass through, leading separators strip, and
// the readable core wraps in '--…--'.
func TestProjectKeyMatchesDSHLayout(t *testing.T) {
	cases := []struct{ cwd, want string }{
		// Observed on the live lane store (DSH_HOME /sessions).
		{"/mnt/workspace/multica_workspaces/moct-27c35c5a56cb/m-141-e723f2a47f3b/workdir",
			"--mnt-workspace-multica_workspaces-moct-27c35c5a56cb-m-141-e723f2a47f3b-workdir--"},
		{"/home/ddv/Documents/deepseek-harness/default-workspace",
			"--home-ddv-Documents-deepseek-harness-default-workspace--"},
		{"/", "--root--"},
		{"/a//b///c", "--a-b-c--"},
		{"C:\\Users\\dev\\proj", "--C-Users-dev-proj--"},
		{"home with spaces/x", "--home~0020with~0020spaces-x--"},
		{"~/dir", "--~007E-dir--"}, // '~' is escaped, not treated as safe
		{"emoji/😀/x", "--emoji-~D83D~DE00-x--"},
	}
	for _, tc := range cases {
		if got := ProjectKey(tc.cwd); got != tc.want {
			t.Errorf("ProjectKey(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

func TestProjectKeyTruncatesToDSHBound(t *testing.T) {
	long := "/" + strings.Repeat("a", 400)
	got := ProjectKey(long)
	inner := strings.TrimSuffix(strings.TrimPrefix(got, "--"), "--")
	if len(inner) > 251 {
		t.Fatalf("inner key length = %d, want <= 251", len([]rune(inner)))
	}
}

func TestValidateSessionID(t *testing.T) {
	for _, ok := range []string{"multica-5b2d76ea-6a66-42e2-ac02-154afa381f0f", "session-b5b5a34a-a312-4a04-99b7-de96df0ed278"} {
		if !ValidateSessionID(ok) {
			t.Errorf("ValidateSessionID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "../etc", "a/b", "a~002F", "a b"} {
		if ValidateSessionID(bad) {
			t.Errorf("ValidateSessionID(%q) = true, want false", bad)
		}
	}
}

// writeSession produces a store directory the way the runtime bridge's
// patched session-persistence-jsonl plugin would: projectKey directory,
// session-id directory, one compressed v4 generation log.
func writeSession(t *testing.T, storeRoot, cwd, sessionID string, lines []string, compress bool) string {
	t.Helper()
	dir := filepath.Join(storeRoot, ProjectKey(cwd), sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "session.v4.jsonl"
	data := []byte(strings.Join(lines, "\n") + "\n")
	if compress {
		name += ".zstd"
		var buf strings.Builder
		w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedFastest))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		data = []byte(buf.String())
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const sampleLog = `{"type":"session","version":4,"id":"session-x","createdAt":1793113801094,"cwd":"/w","isSeeded":false,"delegationDepth":0}
{"type":"permission/preset","seq":0,"time":1,"data":{"preset":"workspace-write"}}
{"type":"user/message","seq":8,"time":10,"data":{"content":[{"type":"text","text":"say hi"}],"source":{"kind":"user"},"role":"user","id":"u1"},"surfaceOp":"append"}
{"type":"assistant/message","seq":18,"time":20,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"reasoning","text":"think"},{"type":"text","text":"hello"},{"type":"tool-call","id":"call_1","name":"bash","arguments":"{}"}]}}}
{"type":"tool/call","seq":19,"time":21,"data":{"turn":1,"step":1,"callId":"call_1","name":"bash","arguments":"{\"command\":\"echo hi\"}"}}
{"type":"tool/result","seq":22,"time":22,"data":{"turn":1,"step":1,"message":{"role":"tool","toolCallId":"call_1","isError":false,"content":[{"type":"text","text":"hi"}]}}}
{"type":"turn/end","seq":30,"time":30,"data":{"turn":1}}
`

func TestReadTranscriptCompressedAndPlain(t *testing.T) {
	store := t.TempDir()
	for _, compress := range []bool{true, false} {
		writeSession(t, store, "/w", "session-x", strings.Split(sampleLog, "\n"), compress)

		events, err := ReadTranscript(store, "/w", "session-x")
		if err != nil {
			t.Fatalf("compress=%v: ReadTranscript: %v", compress, err)
		}
		if len(events) < 6 {
			t.Fatalf("compress=%v: got %d events, want the full log", compress, len(events))
		}
	}
}

func TestTranslate(t *testing.T) {
	store := t.TempDir()
	writeSession(t, store, "/w", "session-x", strings.Split(sampleLog, "\n"), true)

	events, err := ReadTranscript(store, "/w", "session-x")
	if err != nil {
		t.Fatal(err)
	}
	entries := Translate(events)

	want := []Entry{
		{Seq: 1, Type: "thinking", Content: "think", CreatedAt: 20},
		{Seq: 2, Type: "text", Content: "hello", CreatedAt: 20},
		{Seq: 3, Type: "tool_use", Tool: "bash", CallID: "call_1", Input: `{"command":"echo hi"}`, CreatedAt: 21},
		{Seq: 4, Type: "tool_result", CallID: "call_1", Output: "hi", CreatedAt: 22},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %d rows", entries, len(want))
	}
	for i, w := range want {
		got := entries[i]
		if got.Seq != w.Seq || got.Type != w.Type || got.Content != w.Content ||
			got.Tool != w.Tool || got.CallID != w.CallID || got.Input != w.Input ||
			got.Output != w.Output || got.IsError != w.IsError || got.CreatedAt != w.CreatedAt {
			t.Errorf("entry[%d] = %+v, want %+v", i, got, w)
		}
	}
}

// A generation newer than the one a test wrote must win, and the compressed
// twin of the same generation must beat the plaintext one.
func TestNewestGenerationWins(t *testing.T) {
	store := t.TempDir()
	dir := writeSession(t, store, "/w", "session-x", strings.Split(sampleLog, "\n"), true)
	older := filepath.Join(dir, "session.v3.jsonl.zstd")
	if err := os.WriteFile(older, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	events, err := ReadTranscript(store, "/w", "session-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 6 {
		t.Fatalf("newest generation not selected: %d events", len(events))
	}
}

func TestReadTranscriptMissingSession(t *testing.T) {
	store := t.TempDir()
	if _, err := ReadTranscript(store, "/w", "session-absent"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
	if _, err := ReadTranscript(store, "/w", "../escape"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("traversal id err = %v, want ErrNoSession", err)
	}
	if _, err := ReadTranscript("", "/w", "session-x"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no store err = %v, want ErrNoSession", err)
	}
}

// A task's work_dir is only known after the daemon reports it, so the reader
// must also find a session by scanning project directories.
func TestReadTranscriptScansWithoutCwd(t *testing.T) {
	store := t.TempDir()
	writeSession(t, store, "/some/other/project", "session-x", strings.Split(sampleLog, "\n"), true)

	events, err := ReadTranscript(store, "", "session-x")
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if len(events) < 6 {
		t.Fatalf("scan found %d events", len(events))
	}
}

// A live session can be observed mid-append; a truncated final frame must
// fail soft — the readable prefix, or a clean error the caller renders as
// "no transcript yet" — never a panic or an empty non-error.
func TestReadTranscriptToleratesTruncation(t *testing.T) {
	store := t.TempDir()
	writeSession(t, store, "/w", "session-x", strings.Split(sampleLog, "\n"), true)

	path := filepath.Join(store, ProjectKey("/w"), "session-x", "session.v4.jsonl.zstd")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)-8], 0o644); err != nil {
		t.Fatal(err)
	}

	events, err := ReadTranscript(store, "/w", "session-x")
	if err != nil && len(events) != 0 {
		t.Fatalf("truncated read returned %d events with an error: %v", len(events), err)
	}
	if err == nil && len(events) == 0 {
		t.Fatal("truncated read returned no events and no error")
	}
}
