package execenv

import (
	"path/filepath"
	"testing"
)

// TestOpencode2UsesOpenCodePaths pins the identity's task-context surfaces to
// the opencode family: the runtime brief goes to AGENTS.md and skills to
// .opencode/skills, exactly as for the 1.x binary. Both resolve through the
// descriptor's ProtocolFamily / SkillsDir, so this test fails if either is
// wrong in the descriptor.
func TestOpencode2UsesOpenCodePaths(t *testing.T) {
	workDir := t.TempDir()
	if got, want := runtimeConfigPath(workDir, "opencode2"), filepath.Join(workDir, "AGENTS.md"); got != want {
		t.Errorf("runtimeConfigPath(opencode2) = %q, want %q", got, want)
	}
	if got, want := skillsDirPath(workDir, "opencode2"), filepath.Join(workDir, ".opencode", "skills"); got != want {
		t.Errorf("skillsDirPath(opencode2) = %q, want %q", got, want)
	}
}
