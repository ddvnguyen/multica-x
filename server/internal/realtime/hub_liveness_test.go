package realtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The server keeps a client connection alive for `pongWait` and refreshes
// that deadline on any inbound frame. The application-level heartbeat —
// `{"type":"ping"}` answered with `{"type":"pong"}` — is the frame that
// matters in production: it travels as a data frame, so it survives CDNs and
// reverse proxies that terminate the client WebSocket and re-originate it
// (Cloudflare's edge does exactly that), where protocol-level PING/PONG
// control frames are not guaranteed to make it through.
//
// Without the deadline refresh the server tears down a perfectly healthy
// browser connection every pongWait, and the client answers by reconnecting
// and refetching the whole workspace — the "web UI stopped responding"
// symptom.
func TestReadPump_ApplicationPingKeepsConnectionAlive(t *testing.T) {
	restore := shrinkLivenessWindows(t, 1500*time.Millisecond, 300*time.Millisecond)
	defer restore()

	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	waitFor(t, "client registration", func() bool { return totalClients(hub) == 1 })

	// Heartbeat every 400ms for longer than the whole pong window. The
	// client never answers with a protocol-level pong, so the only thing
	// keeping the connection alive is the inbound application ping.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		frame, err := json.Marshal(map[string]any{"type": "ping"})
		if err != nil {
			t.Fatalf("marshal ping: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
			t.Fatalf("write ping after %s: %v", time.Since(deadline.Add(-4*time.Second)), err)
		}

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read pong: %v", err)
		}
		if !strings.Contains(string(raw), "pong") {
			t.Fatalf("got %s, want a pong frame", raw)
		}
	}

	if got := totalClients(hub); got != 1 {
		t.Fatalf("client count = %d, want 1: the application heartbeat did not keep the connection alive", got)
	}
}

// The deadline must still exist: a client that goes completely silent has to
// be disconnected, otherwise dead sockets accumulate on the server.
func TestReadPump_SilentClientIsDisconnected(t *testing.T) {
	restore := shrinkLivenessWindows(t, 1500*time.Millisecond, 300*time.Millisecond)
	defer restore()

	hub, server := newTestHub(t)
	defer server.Close()

	conn := connectWS(t, server)
	defer conn.Close()

	waitFor(t, "client registration", func() bool { return totalClients(hub) == 1 })

	// Do not answer the server's protocol ping and do not send anything.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	waitFor(t, "client to be unregistered", func() bool { return totalClients(hub) == 0 })
}

// shrinkLivenessWindows shrinks the liveness windows for the duration of a
// test and returns a function that restores them.
func shrinkLivenessWindows(t *testing.T, wait, period time.Duration) func() {
	t.Helper()
	oldWait, oldPeriod := pongWait, pingPeriod
	pongWait, pingPeriod = wait, period
	return func() {
		pongWait, pingPeriod = oldWait, oldPeriod
	}
}
