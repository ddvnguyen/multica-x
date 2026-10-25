package daemon

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestProbeAgentCLIs_DiscoversOpenCodeAndOpenCode2Separately places fake
// `opencode` and `opencode2` binaries on PATH and asserts both majors register
// as independent runtimes.
func TestProbeAgentCLIs_DiscoversOpenCodeAndOpenCode2Separately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakeDir := t.TempDir()
	for _, name := range []string{"opencode", "opencode2"} {
		writeDaemonTestExecutable(t, filepath.Join(fakeDir, name), []byte("#!/bin/sh\nexit 0\n"))
	}
	orig := resolveAgentsViaLoginShell
	t.Cleanup(func() { resolveAgentsViaLoginShell = orig })
	resolveAgentsViaLoginShell = func([]string) map[string]string { return map[string]string{} }
	resetShellResolveCacheForTest(t)

	t.Setenv("PATH", fakeDir)
	t.Setenv("MULTICA_OPENCODE_PATH", "")
	t.Setenv("MULTICA_OPENCODE2_PATH", "")

	agents := probeAgentCLIs()
	for name, cmd := range map[string]string{"opencode": "opencode", "opencode2": "opencode2"} {
		e, ok := agents[name]
		if !ok {
			t.Fatalf("%s was not discovered", name)
		}
		if e.Command != cmd {
			t.Errorf("%s command = %q, want %q", name, e.Command, cmd)
		}
	}
	if got := providerDisplayName("opencode2"); got != "OpenCode 2" {
		t.Errorf("display name = %q, want %q", got, "OpenCode 2")
	}
}
