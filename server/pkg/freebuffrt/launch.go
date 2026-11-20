package freebuffrt

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// readyPrefix starts the line the orchestrator prints once it is listening.
const readyPrefix = "[orchestrator-ready] "

// Ready is the orchestrator's startup announcement.
type Ready struct {
	LaunchID string `json:"launchId"`
	PID      int    `json:"pid"`
	Port     int    `json:"port"`
}

// ParseReady parses one stdout line. ok is false for any other line.
func ParseReady(line string) (r Ready, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, readyPrefix) {
		return Ready{}, false
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, readyPrefix)), &r); err != nil {
		return Ready{}, false
	}
	if r.Port <= 0 || r.Port > 65535 {
		return Ready{}, false
	}
	return r, true
}

// NewLaunchID returns a fresh random launch id, the shared secret the caller
// presents on every API request.
func NewLaunchID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// StateFilePath is the orchestrator's state file inside profileDir. The
// orchestrator appends suffixes to it (`<path>.orchestrator-lock.sqlite`,
// `<path>.device-key.json`) and rewrites it on every sign-in, so it must be a
// file path: a directory here reads as unreadable state, which the orchestrator
// reports as "not signed in" and refuses to write a login to.
func StateFilePath(profileDir string) string {
	return filepath.Join(profileDir, "state.json")
}

// LaunchEnv returns the environment variables that make the orchestrator run
// headless with its state under profileDir. The caller merges them over the
// process environment.
//
// FREEBUFF_PROFILE_LOCK_WAIT_MS=0 makes a second orchestrator on the same
// profile fail fast instead of hanging; the profile directory is the account.
func LaunchEnv(b Bundle, profileDir, launchID string) map[string]string {
	return map[string]string{
		"PORT":                           "0",
		"FREEBUFF_LAUNCH_ID":             launchID,
		"FREEBUFF_DESKTOP_STATE_PATH":    StateFilePath(profileDir),
		"FREEBUFF_UI_DIR":                b.UIDir(),
		"CODEBUFF_WASM_DIR":              b.WasmDir(),
		"CODEBUFF_TREE_SITTER_WASM_PATH": filepath.Join(b.WasmDir(), "tree-sitter.wasm"),
		"FREEBUFF_WEB_SERVER":            "1",
		"FREEBUFF_PROFILE_LOCK_WAIT_MS":  "0",
	}
}

// ValidateProfileDir rejects profile directories that are not absolute.
func ValidateProfileDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) {
		return fmt.Errorf("freebuff profile dir must be an absolute path, got %q", dir)
	}
	return nil
}
