package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// pickRuntime builds a runtime row carrying a daemon-reported freebuff block.
func pickRuntime(t *testing.T, provider string, accountIDs ...string) db.AgentRuntime {
	t.Helper()
	block := FreebuffBlock{Accounts: make([]FreebuffAccount, 0, len(accountIDs))}
	for _, id := range accountIDs {
		block.Accounts = append(block.Accounts, FreebuffAccount{ID: id, State: FreebuffAccountConnected})
	}
	raw, err := json.Marshal(map[string]any{"freebuff": block})
	if err != nil {
		t.Fatal(err)
	}
	return db.AgentRuntime{Provider: provider, Metadata: raw}
}

func TestValidateFreebuffAccountPick(t *testing.T) {
	rt := pickRuntime(t, FreebuffProvider, "acct-1", "acct-2")

	if status, msg := validateFreebuffAccountPick(rt, []byte(`{"freebuff":{"account_id":"acct-1"}}`)); status != 0 {
		t.Fatalf("valid pick rejected: %d %s", status, msg)
	}
	if status, msg := validateFreebuffAccountPick(rt, []byte(`{}`)); status != 0 {
		t.Fatalf("no pick rejected: %d %s", status, msg)
	}
	if status, msg := validateFreebuffAccountPick(rt, []byte(`{"other":true}`)); status != 0 {
		t.Fatalf("unrelated config rejected: %d %s", status, msg)
	}
	if status, _ := validateFreebuffAccountPick(rt, []byte(`{"freebuff":{"account_id":"gone"}}`)); status == 0 {
		t.Fatal("unknown account accepted")
	}
	other := pickRuntime(t, "codebuddy", "acct-1")
	if status, _ := validateFreebuffAccountPick(other, []byte(`{"freebuff":{"account_id":"acct-1"}}`)); status == 0 {
		t.Fatal("pick on a non-freebuff runtime accepted")
	}
	broken := pickRuntime(t, FreebuffProvider)
	if status, _ := validateFreebuffAccountPick(broken, []byte(`{"freebuff":{"account_id":"acct-1"}}`)); status == 0 {
		t.Fatal("pick on a runtime with no accounts accepted")
	}
}

func TestStripFreebuffAccountPick(t *testing.T) {
	stripped, changed := stripFreebuffAccountPick([]byte(`{"openclaw":{"mode":"local"},"freebuff":{"account_id":"a"}}`))
	if !changed || stripped == nil {
		t.Fatalf("strip dropped nothing: %q %v", stripped, changed)
	}
	var after map[string]any
	if err := json.Unmarshal(stripped, &after); err != nil {
		t.Fatalf("stripped blob unparsable: %v", err)
	}
	if _, has := after["freebuff"]; has {
		t.Fatalf("freebuff block survived: %v", after)
	}
	if after["openclaw"] == nil {
		t.Fatalf("unrelated keys lost: %v", after)
	}
	if _, changed := stripFreebuffAccountPick([]byte(`{"openclaw":{"mode":"local"}}`)); changed {
		t.Fatal("config without a pick changed")
	}
	if _, changed := stripFreebuffAccountPick(nil); changed {
		t.Fatal("nil config changed")
	}
}
