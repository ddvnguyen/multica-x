package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDshDetachedUnitName(t *testing.T) {
	if got, want := dshDetachedUnit("01a1/task id"), dshDetachedUnitPrefix+"01a1_task_id"; got != want {
		t.Fatalf("unit = %q, want %q", got, want)
	}
	if !strings.HasPrefix(dshDetachedUnit(""), dshDetachedUnitPrefix) {
		t.Fatalf("unit for an empty task id must still be a valid name: %q", dshDetachedUnit(""))
	}
}

// TestDshDetachedArgv pins the launch shape: the command runs inside its own
// transient user scope, through the wrapper that records the exit status, with
// the real executable and its args passed positionally so no re-quoting is
// involved.
func TestDshDetachedArgv(t *testing.T) {
	dir := t.TempDir()
	plan, err := prepareDshDetachedRun(dir, "task-argv")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	argv := plan.argv("/usr/bin/dsh", []string{"--profile", "multica", "--stdio"})
	want := []string{
		"--user", "--scope", "--quiet", "--unit", plan.unit, "--",
		"sh", "-c", `"$@"; status=$?; printf '%d\n' "$status" > "$MULTICA_DETACHED_EXIT"; exit $status`,
		"multica-detached",
		"/usr/bin/dsh", "--profile", "multica", "--stdio",
	}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

// TestDshDetachedExecuteFrameIsStdin proves the child receives the protocol's
// execute frame from a file rather than a daemon pipe.
func TestDshDetachedExecuteFrameIsStdin(t *testing.T) {
	plan, err := prepareDshDetachedRun(t.TempDir(), "task-frame")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	frame := dshExecuteCommand{Version: DshProtocolVersion, Type: "execute", RequestID: "req-1", Prompt: "hello"}
	if err := plan.writeExecuteFrame(frame); err != nil {
		t.Fatalf("writeExecuteFrame: %v", err)
	}
	stdin, err := plan.stdinReader()
	if err != nil {
		t.Fatalf("stdinReader: %v", err)
	}
	defer func() { _ = stdin.Close() }()
	var got dshExecuteCommand
	if err := json.NewDecoder(stdin).Decode(&got); err != nil {
		t.Fatalf("decode frame from stdin: %v", err)
	}
	if got.RequestID != "req-1" || got.Prompt != "hello" {
		t.Fatalf("frame = %+v", got)
	}
}

// TestDshDetachedWrapperRecordsExitStatus runs the wrapper exactly as the scope
// runs it and checks the exit record a recovering daemon reads. No systemd
// involved: this is the wrapper's contract.
func TestDshDetachedWrapperRecordsExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	plan, err := prepareDshDetachedRun(t.TempDir(), "task-exit")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	// Same command line the scope runs, minus systemd-run: "sh -c <script>
	// multica-detached <exec> <args...>".
	argv := plan.argv("/bin/sh", []string{"-c", "exit 7"})[6:]
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "MULTICA_DETACHED_EXIT="+plan.exitPath)
	if err := cmd.Run(); err == nil {
		t.Fatal("wrapper must propagate the child's non-zero status")
	}

	status, ok, err := plan.exitStatus()
	if err != nil {
		t.Fatalf("exitStatus: %v", err)
	}
	if !ok || status != 7 {
		t.Fatalf("exit record = (%d, %v), want (7, true)", status, ok)
	}
}

// TestDshDetachedExitStatusMissingMeansUnfinished is the recovering daemon's
// signal: no record means the wrapper never finished.
func TestDshDetachedExitStatusMissingMeansUnfinished(t *testing.T) {
	plan, err := prepareDshDetachedRun(t.TempDir(), "task-unfinished")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	if _, ok, err := plan.exitStatus(); ok || err != nil {
		t.Fatalf("exitStatus = (_, %v, %v), want (false, nil)", ok, err)
	}
}

// TestDshDetachedStdoutReaderFollowsFile covers the live stream: the reader sees
// bytes appended after it started, and terminates when the run is told the
// process exited.
func TestDshDetachedStdoutReaderFollowsFile(t *testing.T) {
	plan, err := prepareDshDetachedRun(t.TempDir(), "task-tail")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	done := make(chan struct{})
	reader := plan.stdoutReader(done)
	readCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 128)
		for {
			n, err := reader.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				readCh <- string(buf)
				return
			}
		}
	}()

	if _, err := plan.stdoutFile.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * dshDetachedPollInterval)
	if _, err := plan.stdoutFile.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * dshDetachedPollInterval)
	close(done)

	select {
	case got := <-readCh:
		if got != "first\nsecond\n" {
			t.Fatalf("stream = %q, want %q", got, "first\nsecond\n")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tail reader did not terminate after done")
	}
}

// TestDshDetachedRequestedIsOffByDefault guards the opt-in: without the env
// switch nothing changes for every other host.
func TestDshDetachedRequestedIsOffByDefault(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("detached runs are Linux only")
	}
	t.Setenv("MULTICA_DETACHED_RUNTIME", "")
	if dshDetachedRequested() {
		t.Fatal("detached launch must be off unless MULTICA_DETACHED_RUNTIME=1")
	}
	t.Setenv("MULTICA_DETACHED_RUNTIME", "1")
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return
	}
	if !dshDetachedRequested() {
		t.Fatal("detached launch must be on when opted in and systemd-run exists")
	}
}

// TestDshDetachedRunDirKeepsArtifacts checks the artifacts live inside the task
// workdir, so they expire with it and a recovering daemon can find them from
// the task row alone.
func TestDshDetachedRunDirKeepsArtifacts(t *testing.T) {
	cwd := t.TempDir()
	plan, err := prepareDshDetachedRun(cwd, "task-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	for _, path := range []string{plan.stdinPath, plan.stdoutPath, plan.stderrPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("artifact %s: %v", path, err)
		}
		if !strings.HasPrefix(path, filepath.Join(cwd, dshDetachedDir)) {
			t.Fatalf("artifact %s is outside the task workdir", path)
		}
	}
}
