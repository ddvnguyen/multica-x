package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestFreebuffBlockFromRuntime pins how the Freebuff block is read out of a
// runtime row's metadata. The block is written by the daemon (the account
// snapshot) and by the server (the browser-login state), and both must
// survive a read that sees either one.
func TestFreebuffBlockFromRuntime(t *testing.T) {
	cases := []struct {
		name        string
		metadata    string
		wantConnect string
		wantStatus  bool
	}{
		{
			name:     "no metadata at all",
			metadata: "",
		},
		{
			name:     "metadata without a freebuff key",
			metadata: `{"cli_version":"2.168.2"}`,
		},
		{
			name:     "malformed metadata",
			metadata: `{not json`,
		},
		{
			name:        "a connect state only",
			metadata:    `{"freebuff":{"connect":{"state":"awaiting_login","login_url":"https://freebuff.com/login?auth_code=x"}}}`,
			wantConnect: "awaiting_login",
		},
		{
			name:       "an account snapshot only",
			metadata:   `{"freebuff":{"status":{"status":"active","remainingMs":120000}}}`,
			wantStatus: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := db.AgentRuntime{}
			if tc.metadata != "" {
				rt.Metadata = []byte(tc.metadata)
			}
			block := freebuffBlockFromRuntime(rt)
			if tc.wantConnect == "" {
				if block.Connect != nil {
					t.Fatalf("expected no connect state, got %#v", block.Connect)
				}
			} else if block.Connect == nil || block.Connect.State != tc.wantConnect {
				t.Fatalf("connect state = %#v, want %q", block.Connect, tc.wantConnect)
			}
			if tc.wantStatus && len(block.Status) == 0 {
				t.Fatalf("expected an account snapshot, got none")
			}
			if !tc.wantStatus && len(block.Status) > 0 {
				t.Fatalf("expected no account snapshot, got %s", block.Status)
			}
		})
	}
}

// TestFreebuffConnectRequested pins the predicate the daemon polls for: only
// a "requested" state means "start the login". A login that is already
// waiting for the browser, or one that finished, must not start a second.
func TestFreebuffConnectRequested(t *testing.T) {
	cases := map[string]bool{
		`{"freebuff":{"connect":{"state":"requested"}}}`:      true,
		`{"freebuff":{"connect":{"state":"awaiting_login"}}}`: false,
		`{"freebuff":{"connect":{"state":"connected"}}}`:      false,
		`{"freebuff":{"connect":{"state":"failed"}}}`:         false,
		`{"freebuff":{"status":{"status":"active"}}}`:         false,
		`{}`: false,
	}
	for metadata, want := range cases {
		rt := db.AgentRuntime{Metadata: []byte(metadata)}
		if got := freebuffConnectRequested(rt); got != want {
			t.Fatalf("freebuffConnectRequested(%s) = %v, want %v", metadata, got, want)
		}
	}
}

// TestFreebuffBlockPayloadRoundTrip pins that the stored block keeps the
// fields the UI reads: the session slot, the credit, the per-model price and
// the connect state.
func TestFreebuffBlockPayloadRoundTrip(t *testing.T) {
	block := &FreebuffBlock{
		Status: json.RawMessage(`{"status":"active","remainingMs":120000,"credit":{"dailyRemaining":4,"balance":21},"prices":{"z-ai/glm-5.3-flash":2}}`),
		Connect: &FreebuffConnect{
			State:   FreebuffConnectConnected,
			Account: "owner@example.com",
		},
	}
	payload, err := freebuffBlockPayload(block)
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	rt := db.AgentRuntime{Metadata: []byte(`{"freebuff":` + string(payload) + `}`)}
	read := freebuffBlockFromRuntime(rt)
	if read.Connect == nil || read.Connect.State != FreebuffConnectConnected {
		t.Fatalf("connect state lost: %#v", read)
	}
	if read.Connect.Account != "owner@example.com" {
		t.Fatalf("account lost: %#v", read.Connect)
	}
	var status map[string]any
	if err := json.Unmarshal(read.Status, &status); err != nil {
		t.Fatalf("status is not JSON: %v", err)
	}
	if status["remainingMs"] != float64(120000) {
		t.Fatalf("remainingMs = %v, want 120000", status["remainingMs"])
	}
}
