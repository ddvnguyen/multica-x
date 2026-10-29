package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Freebuff account reporting.
//
// A Freebuff runtime's account lives on the daemon's machine: the token file
// is there, and only there can a browser login be completed. The daemon reads
// the account and reports what it finds; the server stores that report in the
// runtime's metadata so the web UI can show it without a second round trip.
//
// Two things travel in the block:
//
//   - "status": the open session slot (opened / expires / remaining), the
//     per-model price, and the credit (daily remaining + limit, and the
//     spendable balance). Reported on a timer, because the session slot
//     counts down whether or not anyone is looking.
//   - "connect": the state of the browser login — "requested" (the user
//     clicked the button), "awaiting_login" (with the URL to open),
//     "connected" (with the account), or "failed" (with the reason).
//
// The block is advisory: it describes an external account the daemon reads,
// not state the server owns. The daemon is the only writer of "status"; the
// server only ever writes "connect" (the click) and the daemon overwrites it
// with the outcome.

// FreebuffBlock is the stored shape of the runtime's `metadata.freebuff` key.
type FreebuffBlock struct {
	// Status is the account snapshot, verbatim from the wrapper's
	// `--status --json`. It stays raw JSON: the wrapper owns that shape, and
	// re-declaring it here would let the two drift.
	Status json.RawMessage `json:"status,omitempty"`
	// Connect is the browser-login state machine.
	Connect *FreebuffConnect `json:"connect,omitempty"`
	// UpdatedAt is when the daemon last reported.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// FreebuffConnect is the state of the browser login.
type FreebuffConnect struct {
	// State is one of "requested", "awaiting_login", "connected", "failed".
	State string `json:"state"`
	// LoginURL is the one-time URL the user opens to sign in. Present while
	// State is "awaiting_login".
	LoginURL string `json:"login_url,omitempty"`
	// ExpiresAt is when that one-time URL stops working. The account host
	// decides it; the UI shows it so the user knows how long they have.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Account identifies the account that signed in.
	Account string `json:"account,omitempty"`
	// Error explains a "failed" state.
	Error string `json:"error,omitempty"`
	// RequestedAt is when the user clicked the button.
	RequestedAt *time.Time `json:"requested_at,omitempty"`
	// UpdatedAt is when the state last changed.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// FreebuffProvider is the provider (protocol family) that makes a runtime a
// Freebuff runtime. The daemon recognises the same value.
const FreebuffProvider = "freebuff"

// Freebuff connect states.
const (
	FreebuffConnectRequested     = "requested"
	FreebuffConnectAwaitingLogin = "awaiting_login"
	FreebuffConnectConnected     = "connected"
	FreebuffConnectFailed        = "failed"
)

// freebuffBlockFromRuntime extracts the Freebuff block from a runtime row's
// metadata. A runtime that never reported one yields an empty block, not an
// error: "no account information yet" is a state the UI renders, not a
// failure.
func freebuffBlockFromRuntime(rt db.AgentRuntime) *FreebuffBlock {
	block := &FreebuffBlock{}
	if len(rt.Metadata) == 0 {
		return block
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(rt.Metadata, &metadata); err != nil {
		return block
	}
	raw, ok := metadata["freebuff"]
	if !ok || len(raw) == 0 {
		return block
	}
	if err := json.Unmarshal(raw, block); err != nil {
		return &FreebuffBlock{}
	}
	return block
}

// freebuffBlockPayload renders a block for storage.
func freebuffBlockPayload(block *FreebuffBlock) ([]byte, error) {
	return json.Marshal(block)
}

// ListFreebuffConnectPending lists the Freebuff runtimes whose browser login
// the user has asked for, and the ones whose login is still waiting for the
// browser.
//
// The account client on the daemon's machine polls this: it is the account
// that owns the login, so it — not the web UI and not the server — is what
// turns a click into a one-time URL. The answer is a short list because a
// login request is rare; the poll is cheap and the daemon's own poll is the
// backstop for a lost nudge.
//
// Member-gated by the router (workspace scope).
func (h *Handler) ListFreebuffConnectPending(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}

	out := make([]map[string]any, 0)
	for _, rt := range runtimes {
		if rt.Provider != FreebuffProvider {
			continue
		}
		block := freebuffBlockFromRuntime(rt)
		if block.Connect == nil {
			continue
		}
		// A login that is already waiting for the browser is listed too: the
		// account client has to keep polling it, and the poll is what turns
		// the browser's approval into credentials.
		if block.Connect.State != FreebuffConnectRequested &&
			block.Connect.State != FreebuffConnectAwaitingLogin {
			continue
		}
		out = append(out, map[string]any{
			"runtime_id":   uuidToString(rt.ID),
			"name":         rt.Name,
			"state":        block.Connect.State,
			"requested_at": block.Connect.RequestedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtimes": out})
}

// ReportFreebuffLogin records the state of a browser login that the account
// client started on the daemon's machine.
//
// The client that owns the account is the one that can complete the
// handshake, so it reports the result here: the one-time URL to open, or the
// account that signed in. The block is merged into the runtime's metadata, so
// the account snapshot the daemon reported is left alone.
//
// Member-gated by the router (workspace scope).
func (h *Handler) ReportFreebuffLogin(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if uuidToString(rt.WorkspaceID) != wsID {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}

	var body struct {
		State     string          `json:"state"`
		LoginURL  string          `json:"login_url"`
		ExpiresAt json.RawMessage `json:"expires_at"`
		Account   string          `json:"account"`
		Error     string          `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	state := strings.TrimSpace(body.State)
	switch state {
	case FreebuffConnectAwaitingLogin, FreebuffConnectConnected, FreebuffConnectFailed:
	case "":
		writeError(w, http.StatusBadRequest, "state is required")
		return
	default:
		writeError(w, http.StatusBadRequest, "unknown state")
		return
	}

	now := time.Now()
	connect := &FreebuffConnect{
		State:     state,
		LoginURL:  body.LoginURL,
		Account:   body.Account,
		Error:     body.Error,
		UpdatedAt: &now,
	}
	if expires, ok := parseFreebuffExpiresAt(body.ExpiresAt); ok {
		connect.ExpiresAt = &expires
	}
	// Carry the original click time forward: the client never saw it.
	if previous := freebuffBlockFromRuntime(rt).Connect; previous != nil {
		connect.RequestedAt = previous.RequestedAt
	}

	payload, err := json.Marshal(map[string]any{"connect": connect})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode the freebuff login state")
		return
	}
	if err := h.Queries.SetAgentRuntimeFreebuffMetadata(r.Context(), db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: payload,
		ID:      rt.ID,
	}); err != nil {
		slog.Warn("freebuff login report failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store the freebuff login state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "connect": connect})
}

// parseFreebuffExpiresAt reads the expiry of a one-time login URL. It
// accepts an RFC 3339 instant (what the API speaks) and epoch milliseconds
// (what the account host reports).
func parseFreebuffExpiresAt(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 {
		return time.Time{}, false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := time.Parse(time.RFC3339, text); err == nil {
			return parsed.UTC(), true
		}
		return time.Time{}, false
	}
	var millis int64
	if err := json.Unmarshal(raw, &millis); err == nil && millis > 0 {
		return time.UnixMilli(millis).UTC(), true
	}
	return time.Time{}, false
}

// GetFreebuffAccount returns the Freebuff account state the daemon last
// reported for a runtime: the open session slot, the per-model price, the
// credit, and the state of the browser login.
//
// The read is cheap (the block rides along in the runtime row) and is meant to
// be polled by the runtime page while a login is in flight.
func (h *Handler) GetFreebuffAccount(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, freebuffBlockFromRuntime(rt))
}

// RequestFreebuffConnect asks the daemon to start a browser login for this
// runtime's Freebuff account.
//
// The click is recorded in the runtime's metadata and the daemon is nudged to
// heartbeat now. The daemon owns the rest: it creates the login URL, reports
// it back, and keeps polling until the browser confirms.
func (h *Handler) RequestFreebuffConnect(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	now := time.Now()
	connect := &FreebuffConnect{
		State:       FreebuffConnectRequested,
		RequestedAt: &now,
		UpdatedAt:   &now,
	}
	// A fresh request clears the previous attempt's outcome: the URL is
	// single-use and the old account may be the one that failed. The account
	// snapshot the daemon reported is left alone — the store merges this
	// write into the block rather than over it.
	payload, err := json.Marshal(map[string]any{"connect": connect})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record the connect request")
		return
	}
	if err := h.Queries.SetAgentRuntimeFreebuffMetadata(r.Context(), db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: payload,
		ID:      rt.ID,
	}); err != nil {
		slog.Warn("freebuff connect request failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record the connect request")
		return
	}

	h.requestDaemonPendingWork(uuidToString(rt.ID), protocol.PendingWorkKindFreebuff)
	writeJSON(w, http.StatusAccepted, connect)
}

// ReportFreebuffAccount is the daemon's report of a runtime's Freebuff
// account: the fresh status snapshot and/or the state of the browser login.
//
// Daemon-authenticated. The payload is merged into the runtime's metadata, so
// a report that carries only a status does not erase a pending login (and the
// other way round).
func (h *Handler) ReportFreebuffAccount(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}

	var report struct {
		Status  json.RawMessage  `json:"status"`
		Connect *FreebuffConnect `json:"connect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	block := &FreebuffBlock{Status: report.Status}
	if report.Connect != nil {
		now := time.Now()
		report.Connect.UpdatedAt = &now
		if report.Connect.RequestedAt == nil {
			// Carry the original click time forward when the daemon does
			// not know it (it only sees the state it was asked to reach).
			if previous := freebuffBlockFromRuntime(rt).Connect; previous != nil {
				report.Connect.RequestedAt = previous.RequestedAt
			}
		}
		block.Connect = report.Connect
	}
	now := time.Now()
	block.UpdatedAt = &now

	payload, err := freebuffBlockPayload(block)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode the freebuff report")
		return
	}
	if err := h.Queries.SetAgentRuntimeFreebuffMetadata(r.Context(), db.SetAgentRuntimeFreebuffMetadataParams{
		Payload: payload,
		ID:      rt.ID,
	}); err != nil {
		slog.Warn("freebuff report failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store the freebuff report")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// GetFreebuffConnectPending answers the daemon's question "did the user ask
// for a browser login?". It is daemon-authenticated and read-only.
//
// The daemon polls this rather than receiving the request in a heartbeat
// acknowledgement: the request lives in the runtime row's metadata, and the
// WebSocket heartbeat path never reads that row. A poll is also the shape
// that survives a lost wakeup hint — the server nudges the daemon over the
// websocket, and the daemon's own poll is the backstop.
func (h *Handler) GetFreebuffConnectPending(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connect_requested": freebuffConnectRequested(rt),
	})
}

// freebuffConnectRequested reports whether the user asked this runtime to
// start a browser login. The daemon uses it to decide to start the login; the
// daemon clears it by reporting the next state.
func freebuffConnectRequested(rt db.AgentRuntime) bool {
	block := freebuffBlockFromRuntime(rt)
	return block.Connect != nil && block.Connect.State == FreebuffConnectRequested
}
