package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// The Freebuff Desktop bridge is the production M-634 case: it enumerates a
// private, opaque-id catalog and answers 400 to an id outside it. A member
// pinned to a retired id must fail before launch with the explicit reason the
// tier cooldown path classifies, so the next resolve falls over.
func TestResolveTaskModelSelectionRejectsStaleModel(t *testing.T) {
	catalog := agent.Catalog{Models: []agent.Model{
		{ID: "m-8db1ccbba6", Label: "DeepSeek V4.1 Flash", Default: true},
		{ID: "m-096e75164d", Label: "DeepSeek V4.1 Flash Fast"},
	}}
	reads := stubModelDiscovery(t, map[string]agent.Catalog{"codebuddy": catalog})
	cmd := agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"}

	in := taskModelSelection{Model: "deepseek-v4.1-flash"}
	sel, err := resolveTaskModelSelection(context.Background(), "codebuddy", cmd, in, quietTaskLog())
	if err == nil {
		t.Fatal("a model absent from a verified catalog must fail the run")
	}
	want := `model "deepseek-v4.1-flash" was not found in the runtime catalog`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := taskRunFailureReason(err); got != taskfailure.ReasonAgentModelNotFoundOrUnavailable.String() {
		t.Errorf("failure reason = %q, want %q", got, taskfailure.ReasonAgentModelNotFoundOrUnavailable.String())
	}
	if reads() != 1 {
		t.Errorf("catalog reads = %d, want 1", reads())
	}
	// The selection is still returned intact for the caller's error path.
	if sel != in {
		t.Errorf("selection on error = %+v, want %+v", sel, in)
	}
}

// A runtime that accepts a free-form model id must never be failed for a
// catalog miss, even when its catalog happens to be verified. Only the
// catalog-authoritative runtimes get the guard.
func TestResolveTaskModelSelectionStaleModelFreeFormProvider(t *testing.T) {
	catalog := agent.Catalog{Models: []agent.Model{{ID: "m-8db1ccbba6", Default: true}}}
	reads := stubModelDiscovery(t, map[string]agent.Catalog{"codebuddy": catalog})
	// A plain codebuddy binary is not the Freebuff Desktop bridge.
	cmd := agent.Command{Path: "/usr/bin/codebuddy"}

	in := taskModelSelection{Model: "deepseek-v4.1-flash"}
	got, err := resolveTaskModelSelection(context.Background(), "codebuddy", cmd, in, quietTaskLog())
	if err != nil {
		t.Fatalf("free-form provider must not fail on a catalog miss: %v", err)
	}
	if got != in {
		t.Errorf("selection = %+v, want %+v unchanged", got, in)
	}
	if reads() != 0 {
		t.Errorf("catalog reads = %d, want 0 — a free-form provider must not pay for discovery", reads())
	}
}

// Discovery that did not verify (empty, static fallback, or an error) says
// nothing about what the runtime accepts: the saved model must go through
// untouched rather than failing the run.
func TestResolveTaskModelSelectionStaleModelUnverifiedCatalog(t *testing.T) {
	cmd := agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"}
	in := taskModelSelection{Model: "deepseek-v4.1-flash"}
	for _, tc := range []struct {
		name    string
		catalog agent.Catalog
		err     error
	}{
		{name: "empty catalog", catalog: agent.Catalog{}},
		{name: "static fallback", catalog: agent.Catalog{Models: []agent.Model{{ID: "stale-static"}}, Fallback: true}},
		{name: "discovery error", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := listModels
			listModels = func(_ context.Context, _ string, _ agent.Command) (agent.Catalog, error) {
				return tc.catalog, tc.err
			}
			t.Cleanup(func() { listModels = orig })

			got, err := resolveTaskModelSelection(context.Background(), "codebuddy", cmd, in, quietTaskLog())
			if err != nil {
				t.Fatalf("unverified catalog must fail open: %v", err)
			}
			if got != in {
				t.Errorf("selection = %+v, want %+v unchanged", got, in)
			}
		})
	}
}

// dsh is deliberately NOT catalog-authoritative here: qualifyDshModelID
// passes ambiguous ids through for the runtime to judge, so rejecting one on
// a catalog miss would break a working selection.
func TestResolveTaskModelSelectionDoesNotRejectDshModel(t *testing.T) {
	catalog := agent.Catalog{Models: []agent.Model{
		{ID: "dsh-opencode-go/beam-free", Default: true},
	}}
	stubModelDiscovery(t, map[string]agent.Catalog{"dsh": catalog})

	in := taskModelSelection{Model: "opencode-go/retired-model"}
	got, err := resolveTaskModelSelection(context.Background(), "dsh", agent.Command{Path: "/usr/bin/dsh"}, in, quietTaskLog())
	if err != nil {
		t.Fatalf("dsh must not fail on a catalog miss: %v", err)
	}
	if got != in {
		t.Errorf("selection = %+v, want %+v unchanged", got, in)
	}
}

// The wrapper rewrites its "no model" sentinel before launching, and the
// picker does not list the sentinel. It must never be treated as stale even
// though the catalog is authoritative and verified.
func TestResolveTaskModelSelectionKeepsFreebuffDefaultSentinel(t *testing.T) {
	catalog := agent.Catalog{Models: []agent.Model{{ID: "m-8db1ccbba6", Default: true}}}
	reads := stubModelDiscovery(t, map[string]agent.Catalog{"codebuddy": catalog})
	cmd := agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"}

	in := taskModelSelection{Model: agent.FreebuffDesktopDefaultModelID}
	got, err := resolveTaskModelSelection(context.Background(), "codebuddy", cmd, in, quietTaskLog())
	if err != nil {
		t.Fatalf("default sentinel must not be rejected: %v", err)
	}
	if got != in {
		t.Errorf("selection = %+v, want %+v unchanged", got, in)
	}
	if reads() != 0 {
		t.Errorf("catalog reads = %d, want 0 for the default sentinel", reads())
	}
}

// Discovery must carry the account env the run will actually use. A bare
// command would enumerate the host's default desktop, which may not be the
// one whose catalog contains (or rejects) the tier's model.
func TestFreebuffDesktopDiscoveryEnvCarriesRunAccount(t *testing.T) {
	runEnv := map[string]string{
		"FREEBUFF_DESKTOP_URL":            "http://127.0.0.1:4320",
		"FREEBUFF_DESKTOP_CREDENTIAL_REF": "/state/account-a",
		freebuffDesktopLaunchIDEnv:        "launch-abc",
		"FREEBUFF_DESKTOP_SCOPE":          "task-identity",
		"MULTICA_WORKSPACE_ID":            "not-a-desktop-knob",
	}
	cmd := freebuffDesktopDiscoveryEnv(runEnv, agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"})

	for _, want := range []string{
		"FREEBUFF_DESKTOP_URL=http://127.0.0.1:4320",
		"FREEBUFF_DESKTOP_CREDENTIAL_REF=/state/account-a",
		"FREEBUFF_DESKTOP_LAUNCH_ID=launch-abc",
	} {
		found := false
		for _, kv := range cmd.Env {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("discovery env = %v, missing %q", cmd.Env, want)
		}
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "FREEBUFF_DESKTOP_SCOPE=") || strings.HasPrefix(kv, "MULTICA_") {
			t.Errorf("discovery env must only carry account knobs, got %q", kv)
		}
	}
	// Deterministic order keeps one cache entry per account.
	again := freebuffDesktopDiscoveryEnv(runEnv, agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"})
	if strings.Join(cmd.Env, "\x00") != strings.Join(again.Env, "\x00") {
		t.Errorf("discovery env is not deterministic: %v vs %v", cmd.Env, again.Env)
	}
}

// The catalog read during a run comes from the env-carrying command, not the
// bare one, so a stale member is judged against the desktop it launches on.
func TestResolveTaskModelSelectionUsesEnvCarriedCatalog(t *testing.T) {
	var gotCmd agent.Command
	orig := listModels
	listModels = func(_ context.Context, _ string, cmd agent.Command) (agent.Catalog, error) {
		gotCmd = cmd
		return agent.Catalog{Models: []agent.Model{{ID: "m-8db1ccbba6", Default: true}}}, nil
	}
	t.Cleanup(func() { listModels = orig })

	cmd := freebuffDesktopDiscoveryEnv(map[string]string{
		"FREEBUFF_DESKTOP_URL": "http://127.0.0.1:4320",
	}, agent.Command{Path: "/opt/freebuff-runtime/freebuff-desktop"})
	_, err := resolveTaskModelSelection(context.Background(), "codebuddy", cmd,
		taskModelSelection{Model: "deepseek-v4.1-flash"}, quietTaskLog())
	if err == nil {
		t.Fatal("expected stale-model failure")
	}
	if !strings.Contains(strings.Join(gotCmd.Env, "\x00"), "FREEBUFF_DESKTOP_URL=http://127.0.0.1:4320") {
		t.Errorf("discovery command lost the run account env: %v", gotCmd.Env)
	}
}
