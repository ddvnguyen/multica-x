package portalsession

// Dedicated internal listener for the trusted-gateway auto-login endpoint
// (M-551). Peer-IP checks cannot work here: public and internal callers
// both arrive with the same SNAT peer behind Caddy and the container
// network, so reachability is the access control instead. The endpoint
// leaves the public router entirely and is served ONLY on this listener,
// bound to 127.0.0.1 by MULTICA_PORTAL_LOGIN_ADDR (off when unset);
// compose publishes that port to the host as 127.0.0.1-only, never
// through Caddy. Rollback: unset MULTICA_PORTAL_LOGIN_ADDR.

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/handler"
)

// NewServer builds the internal-only listener serving POST
// /auth/portal-session from h. Empty addr disables it (nil, nil). Only a
// 127.0.0.1 (or ::1) bind is accepted: anything else would expose the
// secret-bearing endpoint beyond the host, defeating the design.
func NewServer(addr string, h *handler.Handler) (*http.Server, error) {
	if addr == "" {
		return nil, nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("MULTICA_PORTAL_LOGIN_ADDR must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("MULTICA_PORTAL_LOGIN_ADDR must bind 127.0.0.1 or ::1, got %q", host)
	}
	if port == "" {
		return nil, fmt.Errorf("MULTICA_PORTAL_LOGIN_ADDR must include a port")
	}
	mux := chi.NewRouter()
	mux.Post("/auth/portal-session", h.PortalSession)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       15 * time.Second,
	}, nil
}

// Start serves until err, logging like the maintenance listener does.
func Start(srv *http.Server) {
	if srv == nil {
		return
	}
	slog.Info("portal login server starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("portal login listener disabled", "error", err)
	}
}
