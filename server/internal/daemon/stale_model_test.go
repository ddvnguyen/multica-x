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

// dsh is the other catalog-authoritative runtime: its adapter registry
// rejects an unregistered route at dispatch, so a stale member must fail
// over before launch too.
func TestResolveTaskModelSelectionRejectsStaleDshModel(t *testing.T) {
	catalog := agent.Catalog{Models: []agent.Model{
		{ID: "dsh-opencode-go/beam-free", Default: true},
	}}
	stubModelDiscovery(t, map[string]agent.Catalog{"dsh": catalog})

	_, err := resolveTaskModelSelection(context.Background(), "dsh", agent.Command{Path: "/usr/bin/dsh"},
		taskModelSelection{Model: "opencode-go/retired-model"}, quietTaskLog())
	if err == nil || !strings.Contains(err.Error(), "was not found in the runtime catalog") {
		t.Fatalf("stale dsh model error = %v, want model-not-found in runtime catalog", err)
	}
}
