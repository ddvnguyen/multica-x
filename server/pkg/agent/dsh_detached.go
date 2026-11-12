package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Detached dsh runs (S3-C, M-139).
//
// A run launched through `systemd-run --user --scope` executes in its own
// transient systemd scope, so it survives the daemon that started it. Survival
// is only worth anything if the child can keep working once that daemon is
// gone, and the daemon's own pipes cannot provide that: when the daemon dies
// its read ends close, so the child's next write to stdout raises SIGPIPE and
// its stdin reaches EOF. A run that "survives" as a soon-to-die process is not
// a feature.
//
// A detached run therefore binds no daemon pipe at all:
//
//   - stdin is a regular file holding the one `execute` frame this protocol
//     needs up front. The child reads it and reaches EOF, which is what a
//     one-shot run expects; a detached run accepts no further input.
//   - stdout and stderr are regular files in the task's workdir. Nothing the
//     child writes can fail because the daemon left.
//   - the exit status is written by a small shell wrapper, because the process
//     that would normally call Wait and record it may be gone.
//
// The launching daemon still streams the run live by tailing the stdout file,
// and a restarted daemon can read everything the run produced from the same
// files.

// dshDetachedDir is where a detached run keeps those artifacts, relative to the
// task workdir so they live and expire with the run's own directory.
const dshDetachedDir = ".multica-detached"

// dshDetachedUnitPrefix names the transient scope, one unit per task so a
// cancel can stop exactly the run it owns.
const dshDetachedUnitPrefix = "multica-dsh-"

// dshDetachedPollInterval is how often the tail reader re-checks a file the
// child is still appending to. It bounds the added latency of the live stream
// without busy-waiting.
const dshDetachedPollInterval = 50 * time.Millisecond

var dshDetachedUnitUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// dshDetachedRun is one detached launch's file-backed I/O.
type dshDetachedRun struct {
	dir        string
	unit       string
	stdinPath  string
	stdoutPath string
	stderrPath string
	exitPath   string

	stdinFile *os.File
	// stdinRead is the parent's read handle on the frame file, kept only so it
	// can be closed once the child holds its own copy.
	stdinRead  *os.File
	stdoutFile *os.File
	stderrFile *os.File
}

// errDshDetachedNoInput is returned when something tries to write a protocol
// frame into a detached run. Its stdin was the pre-written execute frame; live
// re-attach across daemon restarts is out of scope (S3-C).
var errDshDetachedNoInput = errors.New("detached dsh run does not accept further input")

// DshDetachedAvailable reports whether dsh runs launch in their own systemd
// scope on this host: Linux, MULTICA_DETACHED_RUNTIME set to a true value, and
// systemd-run on PATH. The daemon advertises detached-runtime-v1 under exactly
// this function, so advertising and launching cannot disagree.
//
// The env vocabulary matches the daemon's boolFromEnv: true/1/yes/on, trimmed
// and case-insensitive; unset and unrecognized values are false.
func DshDetachedAvailable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTICA_DETACHED_RUNTIME"))) {
	case "true", "1", "yes", "on":
	default:
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// dshDetachedUnit returns the transient unit name for a task id.
func dshDetachedUnit(taskID string) string {
	name := strings.TrimSpace(taskID)
	if name == "" {
		name = fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return dshDetachedUnitPrefix + dshDetachedUnitUnsafe.ReplaceAllString(name, "_")
}

// prepareDshDetachedRun creates the run directory and its stdio files. cwd is
// the task workdir; the caller owns the returned run and must Close it.
func prepareDshDetachedRun(cwd, taskID string) (*dshDetachedRun, error) {
	if strings.TrimSpace(cwd) == "" {
		return nil, errors.New("detached dsh run requires a workdir")
	}
	dir := filepath.Join(cwd, dshDetachedDir, dshDetachedUnit(taskID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create detached run dir: %w", err)
	}
	run := &dshDetachedRun{
		dir:        dir,
		unit:       dshDetachedUnit(taskID),
		stdinPath:  filepath.Join(dir, "stdin.jsonl"),
		stdoutPath: filepath.Join(dir, "stdout.jsonl"),
		stderrPath: filepath.Join(dir, "stderr.log"),
		exitPath:   filepath.Join(dir, "exit"),
	}
	// Truncate the previous attempt's artifacts: the directory is per task, and
	// a stale exit file would make an unfinished run look finished.
	stdinFile, err := os.OpenFile(run.stdinPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open detached stdin: %w", err)
	}
	stdoutFile, err := os.OpenFile(run.stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdinFile.Close()
		return nil, fmt.Errorf("open detached stdout: %w", err)
	}
	stderrFile, err := os.OpenFile(run.stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdinFile.Close()
		_ = stdoutFile.Close()
		return nil, fmt.Errorf("open detached stderr: %w", err)
	}
	if err := os.Remove(run.exitPath); err != nil && !os.IsNotExist(err) {
		_ = stdinFile.Close()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, fmt.Errorf("clear detached exit record: %w", err)
	}
	run.stdinFile, run.stdoutFile, run.stderrFile = stdinFile, stdoutFile, stderrFile
	return run, nil
}

// argv returns the command line that runs execPath+args inside the scope, with
// the exit status recorded in the run's exit file.
func (r *dshDetachedRun) argv(execPath string, args []string) []string {
	// The wrapper runs the real command as its positional arguments and, when
	// that returns, records the status. `sh -c <script> <name> <args...>` is
	// how the command reaches the script without any re-quoting.
	script := `"$@"; status=$?; printf '%d\n' "$status" > "$MULTICA_DETACHED_EXIT"; exit $status`
	argv := []string{"--user", "--scope", "--quiet", "--unit", r.unit, "--", "sh", "-c", script, "multica-detached"}
	return append(argv, append([]string{execPath}, args...)...)
}

// writeExecuteFrame writes the single protocol frame the child reads from stdin.
func (r *dshDetachedRun) writeExecuteFrame(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode dsh execute command: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := r.stdinFile.Write(encoded); err != nil {
		return fmt.Errorf("write dsh execute command: %w", err)
	}
	// The child needs to read the frame, so hand back a read-only handle from
	// the start of the file.
	if _, err := r.stdinFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind detached stdin: %w", err)
	}
	return nil
}

// stdinReader returns the reader the child receives as stdin. The run keeps the
// handle so releaseStdin and Close can close it.
func (r *dshDetachedRun) stdinReader() (*os.File, error) {
	readOnly, err := os.Open(r.stdinPath)
	if err != nil {
		return nil, fmt.Errorf("open detached stdin for read: %w", err)
	}
	if r.stdinRead != nil {
		_ = r.stdinRead.Close()
	}
	r.stdinRead = readOnly
	return readOnly, nil
}

// releaseStdin closes the parent's read handle and deletes the frame file. The
// frame carries MCP credentials and env, so it must not outlive the moment the
// child holds its own open descriptor; the child keeps reading through that
// descriptor after the unlink.
func (r *dshDetachedRun) releaseStdin(logger *slog.Logger) {
	if r.stdinRead != nil {
		_ = r.stdinRead.Close()
		r.stdinRead = nil
	}
	if err := os.Remove(r.stdinPath); err != nil && !os.IsNotExist(err) && logger != nil {
		logger.Warn("failed to remove detached dsh stdin frame", "path", r.stdinPath, "error", err)
	}
}

// exitStatus reads the wrapper's recorded exit status. A missing file means the
// wrapper never reached its final line, which is the answer a recovering daemon
// needs: the run did not finish.
func (r *dshDetachedRun) exitStatus() (int, bool, error) {
	raw, err := os.ReadFile(r.exitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	var status int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &status); err != nil {
		return 0, false, fmt.Errorf("parse detached exit record %q: %w", strings.TrimSpace(string(raw)), err)
	}
	return status, true, nil
}

// Close releases the file handles and removes the stdin frame, which holds
// secrets and is useless once the run is over (idempotent, so every error path
// is covered). stdout, stderr and the exit record stay on disk: they are the
// run's durable record.
func (r *dshDetachedRun) Close() {
	for _, f := range []*os.File{r.stdinFile, r.stdinRead, r.stdoutFile, r.stderrFile} {
		if f != nil {
			_ = f.Close()
		}
	}
	r.stdinRead = nil
	if err := os.Remove(r.stdinPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove detached dsh stdin frame", "path", r.stdinPath, "error", err)
	}
}

// stdoutReader streams the child's stdout file to the caller, following the
// file as the child appends to it. The returned reader closes when the process
// exits (done is closed) and the file has been drained, so the existing frame
// scanner terminates exactly as it does on a pipe.
func (r *dshDetachedRun) stdoutReader(done <-chan struct{}) io.ReadCloser {
	pr, pw := io.Pipe()
	go tailFileInto(r.stdoutPath, pw, done)
	return pr
}

// stderrTail returns the tail of the child's stderr for error reporting. It is
// called after the process exited, so reading the file to its end is complete.
func (r *dshDetachedRun) stderrTail() string {
	raw, err := os.ReadFile(r.stderrPath)
	if err != nil || len(raw) == 0 {
		return ""
	}
	if len(raw) > agentStderrTailBytes {
		raw = raw[len(raw)-agentStderrTailBytes:]
	}
	return string(raw)
}

// stopScope stops the run's own systemd scope. The scoped child is not in this
// process's group, so a signal to the launcher would leave it running.
func (r *dshDetachedRun) stopScope(logger *slog.Logger) {
	cmd := exec.Command("systemctl", "--user", "stop", r.unit)
	if out, err := cmd.CombinedOutput(); err != nil && logger != nil {
		logger.Warn("failed to stop detached systemd scope", "unit", r.unit, "error", err, "output", strings.TrimSpace(string(out)))
	}
}

// tailFileInto copies bytes appended to path into w until done is closed and the
// file is drained.
//
// The reader opens its own descriptor: the child's writes go through the
// descriptor it inherited, and sharing one file offset between writer and
// reader would make each side's position depend on the other's progress.
func tailFileInto(path string, w *io.PipeWriter, done <-chan struct{}) {
	defer func() { _ = w.Close() }()
	file, err := os.Open(path)
	if err != nil {
		_ = w.CloseWithError(err)
		return
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = w.CloseWithError(readErr)
			return
		}
		select {
		case <-done:
			// The process exited: drain what is left and finish.
			for {
				n, readErr := file.Read(buf)
				if n > 0 {
					if _, writeErr := w.Write(buf[:n]); writeErr != nil {
						return
					}
					continue
				}
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					_ = w.CloseWithError(readErr)
				}
				return
			}
		case <-time.After(dshDetachedPollInterval):
		}
	}
}
