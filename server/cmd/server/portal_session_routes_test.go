package main

// M-551: the public router must NEVER serve the portal auto-login endpoint.
// Reachability is the access control (dedicated 127.0.0.1-only listener),
// so a route on the main mux — reachable via public Caddy and the tailnet —
// is a remote Owner-takeover primitive. This test walks the whole public
// router and fails if POST /auth/portal-session (or any /auth/portal-*
// beyond the known three) is ever mounted there again.
import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
)

func TestPublicRouterHasNoPortalSession(t *testing.T) {
	router := NewRouter(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil)

	var authRoutes []string
	if err := chi.Walk(router, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(pattern, "/auth/") {
			authRoutes = append(authRoutes, method+" "+pattern)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk router: %v", err)
	}
	for _, route := range authRoutes {
		if strings.Contains(route, "portal-session") {
			t.Errorf("public router serves %s; portal-session lives only on the internal listener", route)
		}
	}
	found := map[string]bool{}
	for _, route := range authRoutes {
		found[route] = true
	}
	for _, want := range []string{"POST /auth/send-code", "POST /auth/verify-code", "POST /auth/google"} {
		if !found[want] {
			t.Errorf("public router lost %s; the walk no longer covers auth routes", want)
		}
	}

	// Belt and braces: a direct hit on the public mux 404s (route-not-found),
	// even with the correct secret.
	req := httptest.NewRequest(http.MethodPost, "/auth/portal-session", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Portal-Login-Secret", "anything")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /auth/portal-session on public router = %d, want 404", rec.Code)
	}
}
