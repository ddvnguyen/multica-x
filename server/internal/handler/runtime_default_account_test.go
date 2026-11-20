package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-203: a `default` agent falls back to the runtime's pinned account. The
// pin and its validation are pure, so the whole contract is testable without a
// database: what counts as a pin, and which accounts are usable for one.
func TestRuntimeDefaultAccountPin(t *testing.T) {
	t.Run("unset pin is the legacy path", func(t *testing.T) {
		if got := runtimeDefaultAccountID(runtimeWithMetadata(t, `{}`)); got != "" {
			t.Fatalf("empty metadata pin = %q, want empty", got)
		}
		if got := runtimeDefaultAccountID(runtimeWithMetadata(t, `{"freebuff":{"accounts":[]}}`)); got != "" {
			t.Fatalf("block without a pin = %q, want empty", got)
		}
	})

	t.Run("pin is read from the runtime's own block", func(t *testing.T) {
		const id = "0f4a5f24-0d3b-4a1b-9f9e-2a1c0c9a3b11"
		rt := runtimeWithMetadata(t, `{"freebuff":{"default_account_id":"`+id+`"}}`)
		if got := runtimeDefaultAccountID(rt); got != id {
			t.Fatalf("pin = %q, want %q", got, id)
		}
	})

	t.Run("malformed metadata has no pin", func(t *testing.T) {
		if got := runtimeDefaultAccountID(runtimeWithMetadata(t, `not json`)); got != "" {
			t.Fatalf("malformed metadata pin = %q, want empty", got)
		}
	})
}

// The pinned account is validated exactly like a `specific` selection, because
// the pin is a promise: a claim that cannot honour it must fail closed.
func TestRuntimeDefaultClaimAccount(t *testing.T) {
	runtimeID := parseUUID("11111111-1111-4111-8111-111111111111")
	otherRuntime := parseUUID("22222222-2222-4222-8222-222222222222")

	manual := func() db.WorkspaceAccount {
		return db.WorkspaceAccount{
			ID:            parseUUID("33333333-3333-4333-8333-333333333333"),
			Provider:      accountProviderFreebuffDesktop,
			Enabled:       true,
			Endpoint:      pgtype.Text{String: "https://freebuff.example", Valid: true},
			CredentialRef: pgtype.Text{String: "cred-1", Valid: true},
		}
	}
	managed := func(state string, owner pgtype.UUID) db.WorkspaceAccount {
		acc := manual()
		acc.DaemonAccountID = pgtype.Text{String: "a1b2c3d4", Valid: true}
		acc.RuntimeID = owner
		acc.State = state
		return acc
	}

	cases := []struct {
		name string
		acc  db.WorkspaceAccount
		want bool
	}{
		{"enabled manual account", manual(), true},
		{"disabled account", func() db.WorkspaceAccount { a := manual(); a.Enabled = false; return a }(), false},
		{"wrong provider", func() db.WorkspaceAccount { a := manual(); a.Provider = "codex"; return a }(), false},
		{"managed and connected on this runtime", managed("connected", runtimeID), true},
		{"managed on another runtime", managed("connected", otherRuntime), false},
		{"managed with no owning runtime", managed("connected", pgtype.UUID{}), false},
		{"managed but not connected", managed("signed_out", runtimeID), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acct, ok := runtimeDefaultClaimAccount(runtimeID, tc.acc)
			if ok != tc.want {
				t.Fatalf("ok = %v, want %v", ok, tc.want)
			}
			if !tc.want {
				return
			}
			if acct.ID != tc.acc.ID || acct.Endpoint != tc.acc.Endpoint.String ||
				acct.CredentialRef != tc.acc.CredentialRef.String {
				t.Fatalf("claim account = %+v, want the pinned account's fields", acct)
			}
		})
	}
}

func runtimeWithMetadata(t *testing.T, metadata string) db.AgentRuntime {
	t.Helper()
	return db.AgentRuntime{
		ID:          parseUUID("11111111-1111-4111-8111-111111111111"),
		WorkspaceID: parseUUID("44444444-4444-4444-8444-444444444444"),
		Provider:    accountRuntimeProvider,
		Metadata:    []byte(metadata),
	}
}
