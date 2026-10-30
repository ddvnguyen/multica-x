package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func opencode2TestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestOpencode2RuntimeIdentity exercises the descriptor entry itself: the
// identity must exist as a built-in runtime, not as a protocol family, and it
// must route to the opencode family.
func TestOpencode2RuntimeIdentity(t *testing.T) {
	desc, ok := BuiltinRuntimeByID(opencode2RuntimeID)
	if !ok {
		t.Fatalf("%q is not a registered built-in runtime identity", opencode2RuntimeID)
	}
	if desc.ProtocolFamily != "opencode" {
		t.Fatalf("protocol family = %q, want opencode", desc.ProtocolFamily)
	}
	if desc.DefaultCommand != "opencode2" || desc.DefaultExecutable != "opencode2" {
		t.Fatalf("command/executable = %q/%q, want opencode2/opencode2", desc.DefaultCommand, desc.DefaultExecutable)
	}
	if desc.EnvPrefix != "MULTICA_OPENCODE2" {
		t.Fatalf("env prefix = %q, want MULTICA_OPENCODE2", desc.EnvPrefix)
	}
	if desc.DisplayName != "OpenCode 2" {
		t.Fatalf("display name = %q, want OpenCode 2", desc.DisplayName)
	}
	if desc.ModelDiscovery == nil {
		t.Fatal("opencode2 has no model discovery strategy")
	}
	// An identity is not a protocol family: it must never become a valid
	// protocol_family for a custom runtime profile.
	if IsSupportedType(opencode2RuntimeID) {
		t.Fatal("opencode2 must not be a protocol family")
	}
	if !IsBuiltinRuntime(opencode2RuntimeID) {
		t.Fatal("opencode2 must be reported as a built-in runtime identity")
	}
}

// TestFamilyResolvesRuntimeIdentity keeps the one seam honest: identity in,
// protocol family out, and a family resolved to itself.
func TestFamilyResolvesRuntimeIdentity(t *testing.T) {
	for _, tc := range []struct{ provider, want string }{
		{"opencode2", "opencode"},
		{"opencode", "opencode"},
		{"omp", "pi"},
		{"claude", "claude"},
	} {
		if got := Family(tc.provider); got != tc.want {
			t.Errorf("Family(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

// TestOpencode2InheritsOpenCodeContracts pins every provider-keyed behaviour
// the OpenCode 2 identity has to inherit from the family. Each of these was a
// separate failure before the alias existed: model qualification decides
// whether the daemon reads a catalog before launch, the thinking vocabulary
// decides whether the effort picker renders, and the resume capability decides
// whether a rejected resume is retried.
func TestOpencode2InheritsOpenCodeContracts(t *testing.T) {
	if !ModelSelectorMustBeProviderQualified(opencode2RuntimeID) {
		t.Error("opencode2 must require provider/model qualified selectors like opencode")
	}
	if !ResumeRejectionUndetectable(opencode2RuntimeID) {
		t.Error("opencode2 scrapes its session id like opencode; resume rejection is undetectable")
	}
	if !usesDynamicThinkingCatalog(opencode2RuntimeID) {
		t.Error("opencode2 must resolve its thinking vocabulary from the discovered catalog")
	}
	if got, want := LaunchHeader(opencode2RuntimeID), "opencode2 run (json)"; got != want {
		t.Errorf("LaunchHeader = %q, want %q", got, want)
	}
	for _, blocked := range []string{"--format", "--dir", "--variant", "--dangerously-skip-permissions"} {
		err := ValidateCustomArgs(opencode2RuntimeID, []string{blocked, "x"})
		if err == nil {
			t.Errorf("custom_args %q must stay blocked for opencode2", blocked)
		}
	}
}

// TestNewRuntimeOpencode2BuildsOpenCodeBackend verifies the identity is
// constructible and reports itself, and that the family keeps its own binary.
func TestNewRuntimeOpencode2BuildsOpenCodeBackend(t *testing.T) {
	backend, err := NewRuntime(opencode2RuntimeID, Config{Logger: opencode2TestLogger()})
	if err != nil {
		t.Fatalf("NewRuntime(opencode2): %v", err)
	}
	oc, ok := backend.(*opencodeBackend)
	if !ok {
		t.Fatalf("backend type = %T, want *opencodeBackend", backend)
	}
	if oc.cfg.provider != opencode2RuntimeID {
		t.Fatalf("backend provider = %q, want %q", oc.cfg.provider, opencode2RuntimeID)
	}
	if got := opencodeCommandName(opencode2RuntimeID); got != "opencode2" {
		t.Fatalf("opencodeCommandName(opencode2) = %q, want opencode2", got)
	}
	if got := opencodeCommandName("opencode"); got != "opencode" {
		t.Fatalf("opencodeCommandName(opencode) = %q, want opencode", got)
	}
	// ResolveBackend is the production entry point and has to route the
	// identity through NewRuntime rather than failing in New.
	if _, err := ResolveBackend(opencode2RuntimeID, Config{Logger: opencode2TestLogger()}); err != nil {
		t.Fatalf("ResolveBackend(opencode2): %v", err)
	}
}

// TestResolveOpenCodeExecutableDefaultsToIdentityBinary covers the case the
// daemon hits when a runtime reports no pinned path: the identity must look up
// its own binary, never the 1.x `opencode` one.
func TestResolveOpenCodeExecutableDefaultsToIdentityBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "opencode2")
	writeTestExecutable(t, fake, []byte("#!/bin/sh\nexit 0\n"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := resolveOpenCodeExecutable(Config{provider: opencode2RuntimeID, Logger: opencode2TestLogger()})
	if err != nil {
		t.Fatalf("resolveOpenCodeExecutable(opencode2): %v", err)
	}
	if got != fake {
		t.Fatalf("resolved = %q, want %q", got, fake)
	}
}

// TestListModelsOpencode2ReadsV2Catalog runs the real discovery path for the
// identity: ListModels is keyed by the registered provider, which used to end
// in `unknown agent type: "opencode2"` and left the picker empty.
func TestListModelsOpencode2ReadsV2Catalog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	fixture, err := os.ReadFile("testdata/opencode_v2_models.json")
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "opencode2")
	writeTestExecutable(t, fake, []byte(fmt.Sprintf(`#!/bin/sh
case "$1" in
 --version) echo 'opencode v2.0.32' ;;
 api) cat <<'JSON'
%s
JSON
 ;;
 models)
  if [ "$2" = "--verbose" ]; then
   echo 'Unrecognized flag: --verbose in command opencode models' >&2
   exit 1
  fi
  printf 'opencode-go/deepseek-v4.1-flash\nopencode-go/plain\n'
 ;;
 *) exit 1 ;;
esac
`, fixture)))

	cat, err := ListModels(context.Background(), opencode2RuntimeID, NewCommand(fake, nil))
	if err != nil {
		t.Fatalf("ListModels(opencode2): %v", err)
	}
	if len(cat.Models) != 2 {
		t.Fatalf("models = %+v, want the fixture catalog", cat.Models)
	}
	if cat.Models[0].ID != "opencode-go/deepseek-v4.1-flash" {
		t.Fatalf("first model = %q", cat.Models[0].ID)
	}
}

// TestDiscoverOpenCode2ModelsDefaultsToItsOwnBinary pins the wrapper's one
// job: with no command supplied it must enumerate `opencode2`, not silently
// fall back to the 1.x `opencode` binary the host may also have installed.
func TestDiscoverOpenCode2ModelsDefaultsToItsOwnBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "opencode2")
	writeTestExecutable(t, fake, []byte(`#!/bin/sh
case "$1" in
 --version) echo 'opencode v2.0.32' ;;
 models) printf 'acme/only-from-identity\n' ;;
 *) exit 1 ;;
esac
`))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	models, err := discoverOpenCode2Models(context.Background(), Command{})
	if err != nil {
		t.Fatalf("discoverOpenCode2Models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "acme/only-from-identity" {
		t.Fatalf("models = %+v, want the identity binary's catalog", models)
	}
}

// TestCheckThinkingLevelOpencode2DefaultModelAcceptance covers the thinking
// picker for a model-less agent: opencode accepts a level any advertised model
// supports, and the 2.x identity must answer the same way.
func TestCheckThinkingLevelOpencode2DefaultModelAcceptance(t *testing.T) {
	live := Catalog{Models: []Model{
		{ID: "opencode-go/deepseek-v4.1-flash", Thinking: &ModelThinking{
			SupportedLevels: []ThinkingLevel{{Value: "low"}, {Value: "high"}},
		}},
	}}
	check, err := CheckThinkingLevelWith(func() (Catalog, error) { return live, nil }, opencode2RuntimeID, "", "high")
	if err != nil {
		t.Fatalf("CheckThinkingLevelWith: %v", err)
	}
	if !check.Valid {
		t.Fatalf("thinking level rejected: %+v", check)
	}
	if got := strings.TrimSpace(check.Reason); got != "" {
		t.Fatalf("reason = %q, want none", got)
	}
}
