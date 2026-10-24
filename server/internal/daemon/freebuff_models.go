package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// Freebuff models.
//
// An account's catalog says which models it can run and what an hour of each
// costs in Freebucks. The daemon reads both while it refreshes the account,
// reports them with the account's status for the web UI, and keeps the open
// ones to answer the agent's model picker.

// freebuffModelEntry is one model as reported in an account's status.
type freebuffModelEntry struct {
	// ID is what a task creates its thread with; it is the agent's model value.
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tagline string `json:"tagline,omitempty"`
	Section string `json:"section,omitempty"`
	// Price is Freebucks per hour of session; nil when the account host
	// published none that can be tied to this model.
	Price       *float64 `json:"price"`
	Locked      bool     `json:"locked"`
	Premium     bool     `json:"premium,omitempty"`
	Recommended bool     `json:"recommended,omitempty"`
	// Notes are the host's remarks about the model: promotional, leaving soon,
	// off-peak pricing.
	Notes []string `json:"notes,omitempty"`
}

// freebuffModelCache holds the last catalog read for each account, keyed by
// runtime and account. Reading one needs the account's orchestrator, which a
// running task already holds, so the picker is answered from here.
var freebuffModelCache sync.Map // "runtimeID/accountID" -> []freebuffModelEntry

// freebuffModelEntries joins the catalog with the allowance answer's prices.
// raw may be nil (the account host was unreachable); prices are then unknown.
func freebuffModelEntries(cat freebuffrt.Catalog, allowance json.RawMessage) []freebuffModelEntry {
	var in struct {
		Freebucks struct {
			Prices  map[string]float64 `json:"prices"`
			Notices map[string]string  `json:"priceNotices"`
		} `json:"freebucks"`
	}
	_ = json.Unmarshal(allowance, &in)

	out := make([]freebuffModelEntry, 0, len(cat.Models))
	for _, m := range cat.Models {
		e := freebuffModelEntry{
			ID:          m.ID,
			Name:        m.DisplayName,
			Tagline:     m.Tagline,
			Section:     m.Placement.Section,
			Locked:      !m.Open(),
			Premium:     m.Premium,
			Recommended: m.ID == cat.RecommendedKey,
		}
		if p, ok := freebuffrt.PriceFor(m, in.Freebucks.Prices); ok {
			e.Price = &p
		}
		for _, b := range m.Badges {
			if b.Label != "" {
				e.Notes = append(e.Notes, b.Label)
			}
		}
		if n := in.Freebucks.Notices[m.CompiledID]; m.CompiledID != "" && n != "" {
			e.Notes = append(e.Notes, n)
		}
		out = append(out, e)
	}
	return out
}

func freebuffModelKey(runtimeID, accountID string) string { return runtimeID + "/" + accountID }

// freebuffModelLabel is how the picker names a model: its name and its price.
func freebuffModelLabel(e freebuffModelEntry) string {
	switch {
	case e.Price == nil:
		return e.Name
	case *e.Price == 0:
		return e.Name + " · Free"
	default:
		return e.Name + " · " + strconv.FormatFloat(*e.Price, 'f', -1, 64) + " Freebucks/h"
	}
}

// freebuffPickerModels is the model picker for an agent: the models the
// account can start, with the recommended one as the default.
func freebuffPickerModels(entries []freebuffModelEntry) []agent.Model {
	models := make([]agent.Model, 0, len(entries))
	for _, e := range entries {
		if e.Locked {
			continue
		}
		models = append(models, agent.Model{ID: e.ID, Label: freebuffModelLabel(e), Provider: "freebuff", Default: e.Recommended})
	}
	return models
}

// freebuffRuntimeModels answers the model picker of a Freebuff runtime from
// its default account.
func (d *Daemon) freebuffRuntimeModels(ctx context.Context, rt Runtime) (agent.Catalog, error) {
	accountID, err := agent.FreebuffAccountForRun(agent.Config{RuntimeID: rt.ID})
	if err != nil {
		return agent.Catalog{}, err
	}
	if cached, ok := freebuffModelCache.Load(freebuffModelKey(rt.ID, accountID)); ok {
		return agent.Catalog{Models: freebuffPickerModels(cached.([]freebuffModelEntry))}, nil
	}
	// Not read yet (the daemon just started): ask the account.
	entry := d.freebuffAccountEntry(ctx, rt, accountID)
	if entry["state"] != "connected" {
		return agent.Catalog{}, errors.New("the Freebuff account is not signed in; sign in on the runtime page")
	}
	cached, ok := freebuffModelCache.Load(freebuffModelKey(rt.ID, accountID))
	if !ok {
		return agent.Catalog{}, fmt.Errorf("could not read the Freebuff model catalog for account %s", accountID)
	}
	return agent.Catalog{Models: freebuffPickerModels(cached.([]freebuffModelEntry))}, nil
}

// listRuntimeModels is the model catalog of a runtime: a Freebuff runtime's
// comes from its account, every other from the runtime's own command.
func (d *Daemon) listRuntimeModels(ctx context.Context, rt Runtime, cmd agent.Command) (agent.Catalog, error) {
	if rt.Provider == freebuffProvider {
		return d.freebuffRuntimeModels(ctx, rt)
	}
	return listModels(ctx, rt.Provider, cmd)
}
