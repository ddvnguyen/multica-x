package portalsession

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/handler"
)

// newBareHandler returns a Handler with no Queries: enough for the mux
// shape tests (route present/absent), which never reach the DB.
func newBareHandler() *handler.Handler {
	return &handler.Handler{}
}

func TestNewServerDisabledWhenUnset(t *testing.T) {
	srv, err := NewServer("", newBareHandler())
	if err != nil || srv != nil {
		t.Fatalf("NewServer(\"\") = %v, %v; want nil, nil", srv, err)
	}
}

func TestNewServerBindsLoopback(t *testing.T) {
	srv, err := NewServer("127.0.0.1:18081", newBareHandler())
	if err != nil || srv == nil {
		t.Fatalf("NewServer(loopback) = %v, %v; want server, nil", srv, err)
	}
	if srv.Addr != "127.0.0.1:18081" {
		t.Fatalf("Addr = %q, want 127.0.0.1:18081", srv.Addr)
	}
}

func TestNewServerRejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:18081", "10.0.0.5:18081", "example.com:18081", ":18081", "not-an-addr"} {
		if srv, err := NewServer(addr, newBareHandler()); err == nil || srv != nil {
			t.Errorf("NewServer(%q) = %v, %v; want nil, error", addr, srv, err)
		}
	}
}

func TestPortalMuxServesOnlyPortalSession(t *testing.T) {
	srv, err := NewServer("127.0.0.1:18081", newBareHandler())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mux := srv.Handler

	// POST /auth/portal-session is routed (handler runs; with no env and no
	// Queries it 404s on disabled, which proves the route exists).
	req := httptest.NewRequest("POST", "/auth/portal-session", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("POST /auth/portal-session = %d, want 404 (disabled without env)", rec.Code)
	}

	// Nothing else is mounted: public paths must 404 as route-not-found.
	for _, target := range []string{"GET /api/config", "POST /auth/send-code", "GET /auth/portal-session"} {
		parts := strings.SplitN(target, " ", 2)
		r := httptest.NewRequest(parts[0], parts[1], nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 404 && w.Code != 405 {
			t.Errorf("%s on portal mux = %d, want 404/405", target, w.Code)
		}
	}
}

func TestNilStartIsNoop(t *testing.T) {
	Start(nil) // must not panic
}
