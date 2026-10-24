package freebuffrt

import (
	"context"
	"net/http"
	"testing"
)

// The price table and the catalog names below are the ones a real account
// returned.
var livePrices = map[string]float64{
	"z-ai/glm-5.3-flash": 20, "mimo/mimo-v2.5": 10, "upstage/solar-mini4": 0,
	"fbc/mimo-slow": 5, "fbc/glyph-cluster": 0, "fbc/nemotron-3.5-lightning": 0,
	"fbc/claude-haiku-5.5-100k": 30, "fbc/claude-haiku-5.5": 100,
}

func TestPriceFor(t *testing.T) {
	cases := []struct {
		name  string
		model CatalogModel
		want  float64
		ok    bool
	}{
		{"compiled id", CatalogModel{CompiledID: "z-ai/glm-5.3-flash", DisplayName: "GLM 5.3 Flash"}, 20, true},
		{"free compiled id", CatalogModel{CompiledID: "upstage/solar-mini4", DisplayName: "Solar Mini 4"}, 0, true},
		{"hosted slow", CatalogModel{DisplayName: "MiMo v2.6 Flash Slow"}, 5, true},
		{"hosted stealth", CatalogModel{DisplayName: "Glyph Cluster"}, 0, true},
		{"nemotron", CatalogModel{DisplayName: "Nemotron 3.5 Lightning"}, 0, true},
		{"haiku full context", CatalogModel{DisplayName: "Claude Haiku 5.5 · full context"}, 100, true},
		{"haiku 100k wins the longer slug", CatalogModel{DisplayName: "Claude Haiku 5.5 · 100k context"}, 30, true},
		{"unknown hosted model", CatalogModel{DisplayName: "Brand New Model"}, 0, false},
		{"compiled id missing from the table", CatalogModel{CompiledID: "x/y", DisplayName: "GLM 5.3 Flash"}, 0, false},
	}
	for _, tc := range cases {
		got, ok := PriceFor(tc.model, livePrices)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: PriceFor = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCatalogReadsTheFirstStateSnapshot(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"auth","authed":true}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"state","snapshot":{"freebuff":{"authed":true,"models":[` +
			`{"id":"m-1","compiledId":"z-ai/glm-5.3-flash","displayName":"GLM 5.3 Flash","tagline":"Deep reasoning","access":"open","premium":false,"placement":{"section":"optimized"}},` +
			`{"id":"m-2","compiledId":null,"displayName":"Claude Haiku 5.5","access":"locked","premium":true,"badges":[{"label":"Leaving"}]}` +
			`],"catalog":{"recommendedKey":"m-1"}}}}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	cat, err := c.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Models) != 2 || cat.RecommendedKey != "m-1" {
		t.Fatalf("catalog = %+v", cat)
	}
	if m := cat.Models[0]; m.ID != "m-1" || m.CompiledID != "z-ai/glm-5.3-flash" || !m.Open() || m.Placement.Section != "optimized" {
		t.Fatalf("model 0 = %+v", m)
	}
	if m := cat.Models[1]; m.Open() || !m.Premium || len(m.Badges) != 1 {
		t.Fatalf("model 1 = %+v", m)
	}
}

func TestCatalogRefusesASignedOutAccount(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"state","snapshot":{"freebuff":{"authed":false,"models":[]}}}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	if _, err := c.Catalog(context.Background()); err == nil {
		t.Fatal("a signed-out account returned a catalog")
	}
}
