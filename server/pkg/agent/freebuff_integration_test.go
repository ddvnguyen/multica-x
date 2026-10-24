//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFreebuffRealSmoke runs one turn that needs a tool call on a real,
// signed-in Freebuff account. It spends that account's credit, so it needs
// MULTICA_RUN_REAL_AGENT_SMOKE=1 and a copy of an account directory in
// MULTICA_FREEBUFF_SMOKE_ACCOUNT (a copy: the daemon holds the original).
// MULTICA_FREEBUFF_MODEL names the model key; empty uses Freebuff's default.
func TestFreebuffRealSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run an authenticated Freebuff smoke test")
	}
	account := os.Getenv("MULTICA_FREEBUFF_SMOKE_ACCOUNT")
	if account == "" {
		t.Skip("set MULTICA_FREEBUFF_SMOKE_ACCOUNT to a copy of a signed-in account directory")
	}
	profiles := t.TempDir()
	cfg := Config{RuntimeID: "smoke", Logger: slog.Default(), Env: map[string]string{FreebuffEnvProfilesDir: profiles}}
	dir, err := EnsureFreebuffAccountDir(cfg, "smoke1")
	if err != nil {
		t.Fatal(err)
	}
	copyDir(t, account, dir)

	backend, err := ResolveBackend("freebuff", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer StopFreebuffOrchestrators()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// Two tasks in a row must share one orchestrator, hence one session.
	var pids []int
	for i := 0; i < 2; i++ {
		smokeTurn(t, ctx, backend)
		pids = append(pids, pooledPID(dir))
	}
	if pids[0] == 0 || pids[0] != pids[1] {
		t.Fatalf("second task did not reuse the orchestrator: pids %v", pids)
	}
}

func pooledPID(dir string) int {
	sharedFreebuffPool.mu.Lock()
	defer sharedFreebuffPool.mu.Unlock()
	if e := sharedFreebuffPool.entries[dir]; e != nil && e.orch != nil && e.orch.cmd != nil && e.orch.cmd.Process != nil {
		return e.orch.cmd.Process.Pid
	}
	return 0
}

func smokeTurn(t *testing.T, ctx context.Context, backend Backend) {
	t.Helper()
	session, err := backend.Execute(ctx, "Run the shell command `echo FREEBUFF_SMOKE_OK` with your terminal tool and tell me what it printed.", ExecOptions{
		Cwd:     t.TempDir(),
		Model:   os.Getenv("MULTICA_FREEBUFF_MODEL"),
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := range session.Messages {
			kinds = append(kinds, string(m.Type))
			if m.Type == MessageToolUse {
				t.Logf("tool call: %s %v", m.Tool, m.Input)
			}
			if m.Type == MessageToolResult {
				t.Logf("tool result: %q", m.Output)
			}
		}
	}()
	result := <-session.Result
	<-done
	t.Logf("status=%s model usage=%+v kinds=%v", result.Status, result.Usage, kinds)
	if result.Status != "completed" {
		t.Fatalf("status = %q, error = %q, output = %q", result.Status, result.Error, result.Output)
	}
	if !strings.Contains(result.Output, "FREEBUFF_SMOKE_OK") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
