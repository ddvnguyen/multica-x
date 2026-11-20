package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDshDetachedScopeSurvivesLauncherStop is the S3-C (M-139) restart evidence
// the review asked for, without touching a real daemon.
//
// It reproduces the daemon's situation exactly: a launcher unit (standing in
// for multica-daemon-z) starts the run through the same argv a detached dsh run
// uses, then the launcher unit is stopped the way a daemon restart stops it —
// systemd kills the launcher's cgroup. The run must finish anyway, because it
// lives in its own transient scope and its stdio is file-backed: the exit
// record and the output stream must both be complete afterwards.
//
// This is also the answer to the pipes question: nothing the child writes goes
// into a pipe owned by the launcher, so the launcher's death cannot SIGPIPE it.
func TestDshDetachedScopeSurvivesLauncherStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd user scopes are Linux only")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run not on PATH")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("systemctl not on PATH")
	}
	if err := exec.Command("systemd-run", "--user", "--scope", "--quiet", "--", "true").Run(); err != nil {
		t.Skipf("no usable systemd user manager: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workDir := t.TempDir()
	plan, err := prepareDshDetachedRun(workDir, "scope-test-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()

	// A stand-in for dsh: reads its execute frame from stdin, reports a ready
	// frame, then keeps working after the launcher is gone before finishing.
	fake := filepath.Join(workDir, "fakedsh")
	script := `#!/bin/sh
read -r frame || exit 3
printf '{"version":1,"type":"ready","runtime":"dsh"}\n'
sleep 2
printf '{"version":1,"type":"result","status":"completed"}\n'
exit 0
`
	writeTestExecutable(t, fake, []byte(script))
	if err := plan.writeExecuteFrame(dshExecuteCommand{
		Version: DshProtocolVersion, Type: "execute", RequestID: "scope-test", Prompt: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	launcherUnit := "multica-test-launcher-" + suffix
	childUnit := plan.unit
	t.Cleanup(func() {
		// Stop whatever is left so a failing case cannot leak units.
		_ = exec.Command("systemctl", "--user", "stop", launcherUnit).Run()
		_ = exec.Command("systemctl", "--user", "stop", childUnit).Run()
	})

	// The launcher is a transient service unit that starts the detached run and
	// then just waits, like a daemon holding the run.
	inner := strings.Join([]string{
		"MULTICA_DETACHED_EXIT=" + shSingleQuote(plan.exitPath),
		"systemd-run", "--user", "--scope", "--quiet", "--unit", childUnit, "--",
		"sh", "-c", shSingleQuote(`"$@"; status=$?; printf '%d\n' "$status" > "$MULTICA_DETACHED_EXIT"; exit $status`),
		"multica-detached", shSingleQuote(fake), "--profile", "multica", "--stdio",
		"<", shSingleQuote(plan.stdinPath), ">", shSingleQuote(plan.stdoutPath), "2>", shSingleQuote(plan.stderrPath),
	}, " ")
	if out, err := exec.Command("systemd-run", "--user", "--unit", launcherUnit, "--", "sh", "-c", inner).CombinedOutput(); err != nil {
		t.Fatalf("start launcher unit: %v: %s", err, out)
	}

	// Wait until the run is actually inside its scope before "restarting" the
	// launcher, so this tests survival rather than a race with startup.
	waitFor(t, 15*time.Second, "run to start inside its scope", func() bool {
		raw, err := os.ReadFile(plan.stdoutPath)
		return err == nil && strings.Contains(string(raw), "ready")
	})

	// The restart: systemd kills the launcher's cgroup, exactly as
	// `systemctl --user restart multica-daemon-z` does to the old daemon.
	if err := exec.Command("systemctl", "--user", "stop", launcherUnit).Run(); err != nil {
		t.Fatalf("stop launcher unit: %v", err)
	}

	// The run must finish on its own and leave a complete record behind.
	waitFor(t, 30*time.Second, "the detached run to finish after the launcher stopped", func() bool {
		status, ok, err := plan.exitStatus()
		return err == nil && ok && status == 0
	})
	out, err := os.ReadFile(plan.stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"status":"completed"`) {
		t.Fatalf("output stream incomplete after the restart: %q", string(out))
	}
	if status, ok, _ := plan.exitStatus(); !ok || status != 0 {
		t.Fatalf("exit record = (%d, %v), want (0, true)", status, ok)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func shSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
