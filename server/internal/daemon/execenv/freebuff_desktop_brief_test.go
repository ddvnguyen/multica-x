package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreebuffDesktopUsesAndRestoresProjectInstructions(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "AGENTS.md")
	const existing = "# Project guidance\nKeep these exact words.\n"
	if err := os.WriteFile(project, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := TaskContextForEnv{IssueID: "issue-1", IsFreebuffDesktop: true}
	if _, err := InjectRuntimeConfig(dir, "codebuddy", ctx); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(project)
	if err != nil || !strings.Contains(string(content), existing) || !strings.Contains(string(content), runtimeMarkerBegin) {
		t.Fatalf("project guidance or runtime block missing: %q, %v", content, err)
	}
	if !strings.Contains(string(content), "We are working with Multica workspace") {
		t.Fatal("Desktop introduction missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "CODEBUDDY.md")); !os.IsNotExist(err) {
		t.Fatalf("unexpected separate runtime file: %v", err)
	}
	if err := CleanupRuntimeConfigForTask(dir, "codebuddy", ctx); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(project)
	if err != nil || string(content) != existing {
		t.Fatalf("project guidance changed after cleanup: %q, %v", content, err)
	}
}
