package plannotator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFake installs a shell script standing in for the plannotator binary. The
// real binary is not available in CI (and must not be: the default test suite
// never resolves user-installed agent tooling), so the gate's contract is
// exercised against a script whose exit code and stdout are chosen per case.
func writeFake(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gate is a POSIX shell script")
	}
	path := filepath.Join(t.TempDir(), "plannotator-fake")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	return path
}

func TestArgsFor(t *testing.T) {
	planArgs, err := argsFor(Config{}, Request{Mode: ModePlan, Target: "/tmp/plan.md", ResultFile: "/tmp/verdict.json"})
	if err != nil {
		t.Fatalf("plan args: %v", err)
	}
	want := "annotate /tmp/plan.md --gate --json --require-approval --result-file /tmp/verdict.json"
	if strings.Join(planArgs, " ") != want {
		t.Fatalf("plan args = %q, want %q", strings.Join(planArgs, " "), want)
	}

	diffArgs, err := argsFor(Config{Tailscale: true}, Request{Mode: ModeDiff, Target: "/tmp/run.patch"})
	if err != nil {
		t.Fatalf("diff args: %v", err)
	}
	wantDiff := "review --patch-file /tmp/run.patch --gate --json --tailscale"
	if strings.Join(diffArgs, " ") != wantDiff {
		t.Fatalf("diff args = %q, want %q", strings.Join(diffArgs, " "), wantDiff)
	}

	if _, err := argsFor(Config{}, Request{Mode: ModeDiff, Target: "/tmp/p", ResultFile: "/tmp/r"}); err == nil {
		t.Fatalf("a diff gate must reject --result-file: it is annotate-only")
	}
	if _, err := argsFor(Config{}, Request{Mode: ModePlan, Target: "  "}); err == nil {
		t.Fatalf("a gate with no target must be rejected")
	}
	if _, err := argsFor(Config{}, Request{Mode: "doc", Target: "/tmp/x"}); err == nil {
		t.Fatalf("an unknown mode must be rejected")
	}
}

func TestRunApprovedFromResultFile(t *testing.T) {
	resultFile := filepath.Join(t.TempDir(), "verdict.json")
	fake := writeFake(t, `printf '%s' '{"decision":"approved","feedback":"ship it"}' > "$TEST_RESULT_FILE"
exit 0
`)
	t.Setenv("TEST_RESULT_FILE", resultFile)

	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md", ResultFile: resultFile})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !verdict.Approved() || verdict.Feedback != "ship it" || verdict.ExitCode != 0 {
		t.Fatalf("verdict = %+v", verdict)
	}
}

func TestRunAnnotatedFromStdout(t *testing.T) {
	fake := writeFake(t, `printf '%s\n' '{"decision":"annotated","feedback":"rename the handler"}'
exit 1
`)
	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verdict.Decision != DecisionAnnotated {
		t.Fatalf("decision = %q", verdict.Decision)
	}
	if verdict.Feedback != "rename the handler" {
		t.Fatalf("feedback = %q", verdict.Feedback)
	}
	if verdict.Approved() {
		t.Fatalf("an annotated plan is not an approval")
	}
}

// TestRunReviewUsesMessageField covers the diff surface's payload: `review`
// reports its feedback as "message", not "feedback".
func TestRunReviewUsesMessageField(t *testing.T) {
	fake := writeFake(t, `printf '%s\n' '{"decision":"annotated","message":"missing nil check"}'
exit 1
`)
	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModeDiff, Target: "/tmp/run.patch"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verdict.Feedback != "missing nil check" {
		t.Fatalf("feedback = %q, want the review message", verdict.Feedback)
	}
}

func TestRunDismissed(t *testing.T) {
	fake := writeFake(t, `printf '%s' '{"decision":"dismissed"}'
exit 1
`)
	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verdict.Decision != DecisionDismissed {
		t.Fatalf("decision = %q", verdict.Decision)
	}
	if verdict.Approved() {
		t.Fatalf("a dismissed review is not an approval")
	}
}

// TestRunGateFailure keeps "the gate broke" out of the verdict space: exit 2
// must surface as ErrGateFailed so a caller cannot read it as a denial.
func TestRunGateFailure(t *testing.T) {
	fake := writeFake(t, `exit 2
`)
	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"})
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("err = %v, want ErrGateFailed", err)
	}
	if verdict == nil || verdict.ExitCode != 2 {
		t.Fatalf("verdict = %+v, want the exit code preserved", verdict)
	}
}

// TestRunExitCodeOnly covers a gate whose record never arrived: the documented
// exit codes still yield a decision.
func TestRunExitCodeOnly(t *testing.T) {
	fake := writeFake(t, `exit 0
`)
	verdict, err := Run(context.Background(), Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verdict.Decision != DecisionApproved {
		t.Fatalf("decision = %q, want approved from exit 0", verdict.Decision)
	}
}

func TestRunMissingBinary(t *testing.T) {
	_, err := Run(context.Background(), Config{Binary: "plannotator-not-installed-here"},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"})
	if err == nil {
		t.Fatalf("a missing binary must fail")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v, want the configuration fact named", err)
	}
}

func TestRunTimeout(t *testing.T) {
	fake := writeFake(t, `sleep 5
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, Config{Binary: fake},
		Request{Mode: ModePlan, Target: "/tmp/plan.md"}); err == nil {
		t.Fatalf("a cancelled gate must fail rather than hang")
	}
}

func TestEnvRendering(t *testing.T) {
	got := strings.Join(Config{Remote: true, Origin: "codex", Browser: "none", Port: "19432"}.env(), " ")
	for _, want := range []string{
		"PLANNOTATOR_REMOTE=1",
		"PLANNOTATOR_ORIGIN=codex",
		"PLANNOTATOR_BROWSER=none",
		"PLANNOTATOR_PORT=19432",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("env = %q, missing %q", got, want)
		}
	}
	if len(Config{}.env()) != 0 {
		t.Fatalf("an unconfigured host must set no PLANNOTATOR_* variables: %v", Config{}.env())
	}
}

// TestParseRecordToleratesNoise: a gate may print a session URL before its
// record, and the last JSON object on stdout is the verdict.
func TestParseRecordToleratesNoise(t *testing.T) {
	r := parseRecord([]byte("review at http://localhost:49152\n{\"decision\":\"approved\"}\n"))
	if r == nil || r.Decision != "approved" {
		t.Fatalf("record = %+v", r)
	}
	if parseRecord([]byte("no json here")) != nil {
		t.Fatalf("garbage must not decode as a record")
	}
	if parseRecord(nil) != nil {
		t.Fatalf("empty output must not decode as a record")
	}
}
