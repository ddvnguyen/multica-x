package main

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
)

// The web client (packages/core/api/client.ts) reads and writes an issue's
// owner decisions through the same issue-scoped shape every other issue
// resource uses: GET /api/issues/{id}/decision-records and
// POST /api/issues/{id}/decision-response. Mounted only under the workspace
// group, the detail page's decisions panel answers "Could not load decisions"
// on every issue (M-522). Pin the convention here so it cannot drift again
// unnoticed by a live click.
func TestIssueDecisionRoutesMatchClientPaths(t *testing.T) {
	router := NewRouter(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)

	mounted := map[string]bool{}
	err := chi.Walk(router, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[method+" "+pattern] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk router: %v", err)
	}

	want := []string{
		"GET /api/issues/{id}/decision-records",
		"POST /api/issues/{id}/decision-response",
	}
	for _, route := range want {
		if !mounted[route] {
			t.Errorf("%s is not mounted; the client calls it, so the issue detail renders \"Could not load decisions\"", route)
		}
	}

	// One mount, not two: the workspace-scoped twin is the half that drifted
	// from the convention, so it must not come back beside the issue-scoped
	// route.
	unwanted := []string{
		"GET /api/workspaces/{id}/issues/{issueId}/decision-records",
		"POST /api/workspaces/{id}/issues/{issueId}/decision-response",
	}
	for _, route := range unwanted {
		if mounted[route] {
			t.Errorf("%s is mounted beside the issue-scoped route; issue resources live under /api/issues/{id}", route)
		}
	}
}
