package agent

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFakeBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenCodeFamilyResolvesItsOwnDefaultExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	dir := t.TempDir()
	want1 := writeFakeBinary(t, dir, "opencode")
	want2 := writeFakeBinary(t, dir, "opencode2")
	t.Setenv("PATH", dir)

	for _, tc := range []struct {
		provider, want string
		build          func(string, Config) (Backend, error)
	}{
		{"opencode", want1, New},
		{"opencode2", want2, NewRuntime},
	} {
		b, err := tc.build(tc.provider, Config{Logger: slog.Default()})
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		ob, ok := b.(*opencodeBackend)
		if !ok {
			t.Fatalf("%s: got %T, want *opencodeBackend", tc.provider, b)
		}
		got, err := resolveOpenCodeExecutable(ob.cfg, ob.defaultExecutable)
		if err != nil || got != tc.want {
			t.Errorf("%s: resolved %q, %v; want %q", tc.provider, got, err, tc.want)
		}
		if ob.providerLabel != tc.provider {
			t.Errorf("%s: providerLabel = %q", tc.provider, ob.providerLabel)
		}
	}
}

func TestOpenCode2IdentityPinsV2Contract(t *testing.T) {
	b, err := NewRuntime("opencode2", Config{Logger: slog.Default(), BuiltinRuntime: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := b.(*opencodeBackend).cfg
	if !opencodeUsesV2Contract(cfg) {
		t.Error("opencode2 must use the 2.x contract even with no detected version")
	}
	if opencodeUsesV2Contract(Config{BuiltinRuntime: true}) {
		t.Error("opencode with no detected version must keep the 1.x contract")
	}
	if got := opencodeDefaultExecutable(cfg); got != "opencode2" {
		t.Errorf("clock-check fallback = %q, want opencode2", got)
	}
	if IsSupportedType("opencode2") {
		t.Error("opencode2 is a runtime identity, not a protocol family")
	}
}

func TestOpenCode2MinVersionAndThinking(t *testing.T) {
	if err := CheckMinVersion("opencode2", "opencode v2.1.4"); err != nil {
		t.Errorf("2.1.4 rejected: %v", err)
	}
	if err := CheckMinVersion("opencode2", "1.19.3"); err == nil {
		t.Error("1.19.3 must be rejected for the opencode2 runtime")
	}
	if !ThinkingControlSupported("opencode2") || !IsKnownThinkingValue("opencode2", "high") {
		t.Error("opencode2 must use the dynamic thinking catalog")
	}
	if !ResumeRejectionUndetectable("opencode2") {
		t.Error("opencode2 shares opencode's stream-scraping backend")
	}
}
