//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFreebuffDesktopModels(t *testing.T) {
	models, err := parseFreebuffDesktopModels([]byte(`{"models":[
		{"id":"a","name":"Model A","provider":"freebuff"},
		{"id":"a","name":"dup"},
		{"id":"","name":"no id"},
		{"id":"b","label":"Model B"},
		{"id":"c"}
	],"supported":true}`))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, m := range models {
		got = append(got, m.ID+"="+m.Label)
	}
	want := "a=Model A,b=Model B,c=c"
	if strings.Join(got, ",") != want {
		t.Fatalf("models = %v, want %s", got, want)
	}
	if models[0].Provider != "freebuff" {
		t.Fatalf("provider = %q", models[0].Provider)
	}
	if _, err := parseFreebuffDesktopModels([]byte("not json")); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestCommandWithEnvDoesNotAliasAndKeysTheCache(t *testing.T) {
	base := NewCommand("/bin/freebuff-desktop", nil)
	a := base.WithEnv("FREEBUFF_DESKTOP_URL=http://a")
	b := base.WithEnv("FREEBUFF_DESKTOP_URL=http://b")
	if len(base.Env) != 0 {
		t.Fatalf("WithEnv mutated the receiver: %v", base.Env)
	}
	if a.cacheKey() == b.cacheKey() || a.cacheKey() == base.cacheKey() {
		t.Fatal("commands with different env must not share a discovery cache key")
	}
	if v, ok := b.envValue("FREEBUFF_DESKTOP_URL"); !ok || v != "http://b" {
		t.Fatalf("envValue = %q, %v", v, ok)
	}
}

func TestIsFreebuffDesktopCommand(t *testing.T) {
	cases := []struct {
		cmd  Command
		want bool
	}{
		{NewCommand("/usr/local/bin/freebuff-desktop", nil), true},
		{NewCommand("/usr/local/bin/freebuff-desktop-bridge.mjs", nil), true},
		{NewCommand("codebuddy", nil), false},
		{NewCommand("codebuddy", nil).WithEnv("FREEBUFF_DESKTOP_URL=http://x"), true},
	}
	for _, c := range cases {
		if got := isFreebuffDesktopCommand(c.cmd); got != c.want {
			t.Errorf("isFreebuffDesktopCommand(%v) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// A fake bridge that reports a different model per account proves the account
// environment reaches the process and that two accounts do not share a cached
// catalog.
func TestListModelsFreebuffDesktopIsPerAccount(t *testing.T) {
	dir := t.TempDir()
	bridge := filepath.Join(dir, "freebuff-desktop")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--list-models" ]; then ` +
		`printf '{"models":[{"id":"model-for-%s","name":"M"}]}\n' "$(printf %s "$FREEBUFF_DESKTOP_URL" | tr -c 'a-z0-9\n' '-')"; exit 0; fi` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(bridge, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	base := NewCommand(bridge, nil)

	ids := func(url string) string {
		t.Helper()
		cat, err := ListModels(context.Background(), "codebuddy", base.WithEnv("FREEBUFF_DESKTOP_URL="+url))
		if err != nil {
			t.Fatalf("ListModels(%s): %v", url, err)
		}
		if len(cat.Models) != 1 {
			t.Fatalf("models for %s = %+v", url, cat.Models)
		}
		return cat.Models[0].ID
	}
	a, b := ids("http://a.test"), ids("http://b.test")
	if a == b {
		t.Fatalf("accounts share a catalog: %q == %q", a, b)
	}
	if again := ids("http://a.test"); again != a {
		t.Fatalf("repeat discovery changed: %q != %q", again, a)
	}
}

func TestListModelsFreebuffDesktopFailureIsNotMasked(t *testing.T) {
	dir := t.TempDir()
	bridge := filepath.Join(dir, "freebuff-desktop")
	if err := os.WriteFile(bridge, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ListModels(context.Background(), "codebuddy", NewCommand(bridge, nil).WithEnv("FREEBUFF_DESKTOP_URL=http://fail.test"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the bridge's stderr", err)
	}
}
