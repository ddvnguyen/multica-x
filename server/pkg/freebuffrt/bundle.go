package freebuffrt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Bundle is a Freebuff Desktop resources directory (or an export of one).
// Its orchestrator/ subdirectory holds orchestrator.js, ui/ and wasm/.
type Bundle struct {
	// ResourcesDir is the directory that contains orchestrator/.
	ResourcesDir string
}

// OrchestratorPath is the JavaScript entry point to run with Bun.
func (b Bundle) OrchestratorPath() string {
	return filepath.Join(b.ResourcesDir, "orchestrator", "orchestrator.js")
}

// UIDir is the directory of static UI assets the orchestrator serves.
func (b Bundle) UIDir() string { return filepath.Join(b.ResourcesDir, "orchestrator", "ui") }

// WasmDir is the directory of tree-sitter grammars.
func (b Bundle) WasmDir() string { return filepath.Join(b.ResourcesDir, "orchestrator", "wasm") }

// Environment variables that locate the bundle, in resolution order.
const (
	// EnvRuntimeDir points at an exported runtime (freebuff/server/runtime).
	EnvRuntimeDir = "FREEBUFF_RUNTIME_DIR"
	// EnvAppPath points at an installed Desktop app, its Resources directory,
	// or orchestrator.js itself.
	EnvAppPath = "FREEBUFF_APP_PATH"
)

// ErrBundleNotFound is returned when no bundle is installed or configured.
var ErrBundleNotFound = errors.New("freebuff orchestrator bundle not found")

// ResolveBundle finds the orchestrator bundle. env is consulted before the
// well-known install locations; home is the user's home directory.
func ResolveBundle(env map[string]string, home string) (Bundle, error) {
	var candidates []string
	if dir := strings.TrimSpace(env[EnvRuntimeDir]); dir != "" {
		candidates = append(candidates, dir)
	}
	if app := strings.TrimSpace(env[EnvAppPath]); app != "" {
		candidates = append(candidates, appPathCandidates(app)...)
	}
	candidates = append(candidates, installCandidates(home)...)

	for _, dir := range candidates {
		b := Bundle{ResourcesDir: dir}
		if fi, err := os.Stat(b.OrchestratorPath()); err == nil && !fi.IsDir() {
			return b, nil
		}
	}
	return Bundle{}, fmt.Errorf("%w: set %s to an exported runtime or %s to a Freebuff Desktop install",
		ErrBundleNotFound, EnvRuntimeDir, EnvAppPath)
}

func appPathCandidates(app string) []string {
	switch {
	case strings.HasSuffix(app, "orchestrator.js"):
		return []string{filepath.Dir(filepath.Dir(app))}
	case strings.HasSuffix(app, ".app"):
		return []string{filepath.Join(app, "Contents", "Resources")}
	case filepath.Base(app) == "orchestrator":
		return []string{filepath.Dir(app)}
	default:
		return []string{
			filepath.Join(app, "Contents", "Resources"),
			filepath.Join(app, "resources"),
			app,
		}
	}
}

func installCandidates(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Freebuff.app/Contents/Resources",
			filepath.Join(home, "Applications", "Freebuff.app", "Contents", "Resources"),
		}
	case "windows":
		var out []string
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			out = append(out, filepath.Join(v, "Programs", "Freebuff", "resources"))
		}
		if v := os.Getenv("PROGRAMFILES"); v != "" {
			out = append(out, filepath.Join(v, "Freebuff", "resources"))
		}
		return out
	default:
		return []string{
			"/opt/Freebuff/resources",
			"/opt/freebuff/resources",
			"/usr/lib/freebuff/resources",
			filepath.Join(home, ".local", "share", "freebuff", "resources"),
		}
	}
}
