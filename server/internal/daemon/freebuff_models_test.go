package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

func TestFreebuffRuntimeAccountDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"absent", ``, ""},
		{"other provider block", `{"openclaw":{"mode":"local"}}`, ""},
		{"valid pick", `{"freebuff":{"account_id":"acct-1"}}`, "acct-1"},
		{"picked with spaces", `{"freebuff":{"account_id":" acct-1 "}}`, "acct-1"},
		{"cleared pick", `{"freebuff":{"account_id":""}}`, ""},
		{"malformed", `{"freebuff":{"account_id":`, ""},
	} {
		if got := freebuffRuntimeAccount([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: pick = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// accountWithCatalog builds a fake signed-in account advertising one catalog.
func accountWithCatalog(models ...freebuffrt.ModelCatalogEntry) *fakeAccount {
	f := &fakeAccount{
		statuses: []freebuffrt.AuthStatus{signedIn("a@b.c")},
	}
	var cat freebuffrt.ModelCatalog
	cat.Models = models
	cat.Default = models[0].ID
	f.catalogs = []freebuffrt.ModelCatalog{cat}
	return f
}

func TestDiscoverFreebuffCatalogsMergesAccounts(t *testing.T) {
	got, err := discoverFreebuffCatalogs(context.Background(), "rt-fb", []string{"a", "b"},
		func(_ context.Context, _, accountID string) (freebuffAccount, func(), error) {
			switch accountID {
			case "a":
				return accountWithCatalog(
					freebuffrt.ModelCatalogEntry{ID: "shared", DisplayName: "Shared"},
					freebuffrt.ModelCatalogEntry{ID: "solar"},
				), func() {}, nil
			case "b":
				return accountWithCatalog(
					freebuffrt.ModelCatalogEntry{ID: "shared"},
					freebuffrt.ModelCatalogEntry{ID: "luna"},
				), func() {}, nil
			}
			t.Fatalf("unexpected account %q", accountID)
			return nil, nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 3 {
		t.Fatalf("models = %+v", got.Models)
	}
	shared := got.Models[0]
	if shared.ID != "shared" || len(shared.AccountIDs) != 2 || shared.AccountIDs[0] != "a" {
		t.Fatalf("shared model = %+v", shared)
	}
	if !shared.Default {
		t.Fatalf("shared model should carry the default badge — the first account recommends it: %+v", shared)
	}
	if got.Failed["a"] != nil || got.Failed["b"] != nil {
		t.Fatalf("unexpected failures: %v", got.Failed)
	}
}

func TestDiscoverFreebuffCatalogsSkipsAFailedAccount(t *testing.T) {
	launched := []string{}
	got, err := discoverFreebuffCatalogs(context.Background(), "rt-fb", []string{"a", "b"},
		func(_ context.Context, _, accountID string) (freebuffAccount, func(), error) {
			launched = append(launched, accountID)
			if accountID == "a" {
				return nil, nil, errors.New("boot failed")
			}
			return accountWithCatalog(freebuffrt.ModelCatalogEntry{ID: "luna"}), func() {}, nil
		})
	if err != nil {
		t.Fatalf("one failing account must not fail the request: %v", err)
	}
	if got.Failed["a"] == nil {
		t.Fatalf("failure not recorded: %+v", got.Failed)
	}
	if len(launched) != 2 {
		t.Fatalf("launches = %v", launched)
	}
}

func TestDiscoverFreebuffCatalogsFailsWhenEveryAccountFails(t *testing.T) {
	_, err := discoverFreebuffCatalogs(context.Background(), "rt-fb", []string{"a", "b"},
		func(context.Context, string, string) (freebuffAccount, func(), error) {
			return nil, nil, errors.New("down")
		})
	if err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("err = %v, want the account failure", err)
	}
}

func TestDiscoverFreebuffCatalogsTreatsSignedOutAsFailedAccount(t *testing.T) {
	f := &fakeAccount{statuses: []freebuffrt.AuthStatus{{Authed: false}}}
	_, err := discoverFreebuffCatalogs(context.Background(), "rt-fb", []string{"a"},
		func(context.Context, string, string) (freebuffAccount, func(), error) {
			return f, func() {}, nil
		})
	if !errors.Is(err, errAccountSignedOut) {
		t.Fatalf("err = %v, want %v", err, errAccountSignedOut)
	}
}

func TestHandleModelListForFreebuffRuntime(t *testing.T) {
	fx := newModelListFixture(t)
	d := fx.daemon
	useProfiles(t)
	rt := freebuffRuntime()
	for _, id := range []string{"a", "b"} {
		if _, err := agent.EnsureFreebuffAccountDir(agent.Config{RuntimeID: rt.ID}, id); err != nil {
			t.Fatal(err)
		}
	}
	// Two launches, one fake, ordered answers: each account advertises "luna".
	fa := &fakeAccount{
		statuses: []freebuffrt.AuthStatus{signedIn("a@b.c"), signedIn("a@b.c")},
	}
	cat := []freebuffrt.ModelCatalog{
		{Models: []freebuffrt.ModelCatalogEntry{{ID: "luna", DisplayName: "Luna 2"}}, Default: "luna"},
		{Models: []freebuffrt.ModelCatalogEntry{{ID: "luna", DisplayName: "Luna 2"}}, Default: "luna"},
	}
	fa.catalogs = cat
	useFakeAccount(t, fa, nil)
	d.runtimeIndex[rt.ID] = rt
	d.handleModelList(context.Background(), rt, "req-1", nil)
	fx.mu.Lock()
	defer fx.mu.Unlock()
	report := fx.report
	if report["status"] != "completed" {
		t.Fatalf("report status = %v (report: %v)", report["status"], report)
	}
	models, ok := report["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("report models = %T %v, want one merged row", report["models"], report["models"])
	}
	model := models[0].(map[string]any)
	if model["id"] != "luna" {
		t.Fatalf("reported model = %v", model)
	}
	if ids := model["account_ids"].([]any); len(ids) != 2 {
		t.Fatalf("account_ids = %v, want both accounts", model["account_ids"])
	}
	if got := report["supported"]; got != true {
		t.Fatalf("supported = %v, want true", got)
	}
}

func TestHandleModelListForFreebuffRuntimeWithoutAccounts(t *testing.T) {
	fx := newModelListFixture(t)
	d := fx.daemon
	useProfiles(t)
	rt := freebuffRuntime()
	d.runtimeIndex[rt.ID] = rt
	d.handleModelList(context.Background(), rt, "req-1", nil)
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if fx.report["status"] != "completed" {
		t.Fatalf("report status = %v (report: %v)", fx.report["status"], fx.report)
	}
	models, ok := fx.report["models"].([]any)
	if !ok || len(models) != 0 {
		t.Fatalf("models = %T %v, want none", fx.report["models"], fx.report["models"])
	}
	if got := fx.report["supported"]; got != true {
		t.Fatalf("supported = %v, want true", got)
	}
}
