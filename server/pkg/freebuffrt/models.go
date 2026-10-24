package freebuffrt

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CatalogModel is one model of the account's Freebuff catalog.
type CatalogModel struct {
	// ID is the key a thread is created with.
	ID          string `json:"id"`
	CompiledID  string `json:"compiledId"`
	DisplayName string `json:"displayName"`
	Tagline     string `json:"tagline"`
	// Access is "open" for a model the account can start and "locked" for one
	// that needs a plan.
	Access   string `json:"access"`
	Premium  bool   `json:"premium"`
	DataUse  string `json:"dataUse"`
	Badges   []struct {
		Label string `json:"label"`
	} `json:"badges"`
	Placement struct {
		Section     string `json:"section"`
		Recommended bool   `json:"recommended"`
	} `json:"placement"`
}

// Open reports whether the account can start this model.
func (m CatalogModel) Open() bool { return m.Access != "locked" }

// Catalog is the account's model catalog and the model Freebuff recommends.
type Catalog struct {
	Models         []CatalogModel
	RecommendedKey string
}

// catalogTimeout bounds the wait for the orchestrator's first state snapshot.
const catalogTimeout = 15 * time.Second

// Catalog reads the model catalog. The orchestrator publishes it only inside
// the state snapshot that opens GET /api/events, so this reads that one event.
func (c *Client) Catalog(ctx context.Context) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	events, wait, err := c.Events(ctx)
	if err != nil {
		return Catalog{}, err
	}
	defer func() {
		cancel()
		_ = wait()
	}()
	for ev := range events {
		if ev.Type != "state" {
			continue
		}
		var env struct {
			Snapshot struct {
				Freebuff struct {
					Authed  bool           `json:"authed"`
					Models  []CatalogModel `json:"models"`
					Catalog struct {
						RecommendedKey string `json:"recommendedKey"`
					} `json:"catalog"`
				} `json:"freebuff"`
			} `json:"snapshot"`
		}
		if json.Unmarshal(ev.Raw, &env) != nil {
			continue
		}
		fb := env.Snapshot.Freebuff
		if !fb.Authed {
			return Catalog{}, errors.New("freebuff account is not signed in")
		}
		return Catalog{Models: fb.Models, RecommendedKey: fb.Catalog.RecommendedKey}, nil
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	return Catalog{}, errors.New("freebuff orchestrator sent no model catalog")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9.]+`)

func tokens(s string) []string {
	return strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

// PriceFor finds the price of m in the account's price table, in Freebucks
// per hour. The table is keyed by the model's compiled id; models without one
// (the Freebuff-hosted ones) are keyed "fbc/<slug>", and the catalog does not
// say which slug is whose. Those are matched by name: the slug's words must
// all appear in the model's name, and the slug with the most words wins.
// ok is false when no price can be assigned with confidence.
func PriceFor(m CatalogModel, prices map[string]float64) (price float64, ok bool) {
	if m.CompiledID != "" {
		price, ok = prices[m.CompiledID]
		return price, ok
	}
	name := map[string]bool{}
	for _, t := range tokens(m.DisplayName) {
		name[t] = true
	}
	best, bestWords := "", 0
	keys := make([]string, 0, len(prices))
	for k := range prices {
		if strings.HasPrefix(k, "fbc/") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		words := tokens(strings.TrimPrefix(k, "fbc/"))
		match := len(words) > 0
		for _, w := range words {
			if !name[w] {
				match = false
				break
			}
		}
		if match && len(words) > bestWords {
			best, bestWords = k, len(words)
		}
	}
	if best == "" {
		return 0, false
	}
	return prices[best], true
}
