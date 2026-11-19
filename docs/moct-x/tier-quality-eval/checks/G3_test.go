package claim

import (
	"net/http"
	"testing"
	"time"
)

func TestParseCooldownDeltaSeconds(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got := ParseCooldown(now, "120")
	if !got.Equal(now.Add(120 * time.Second)) {
		t.Fatalf("want now+120s got %v", got)
	}
}

func TestParseCooldownFallsBackTo15m(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, v := range []string{"", "  ", "soon", "-30"} {
		got := ParseCooldown(now, v)
		if !got.Equal(now.Add(15 * time.Minute)) {
			t.Fatalf("retryAfter %q must fall back to 15m, got %v", v, got)
		}
	}
}

func TestParseCooldownHTTPDate(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := now.Add(90 * time.Second).UTC().Truncate(time.Second)
	got := ParseCooldown(now, want.Format(http.TimeFormat))
	if diff := got.Sub(want); diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("HTTP-date: want ~%v got %v", want, got)
	}
}

func TestPickSkipsCoolingTier(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tiers := []Tier{
		{ID: "a", Idx: 0, Enabled: true},
		{ID: "b", Idx: 1, Enabled: true},
	}
	cooling := map[string]time.Time{"a": now.Add(5 * time.Minute)}
	if id, ok := Pick(tiers, cooling, now); !ok || id != "b" {
		t.Fatalf("want b,true got %q,%v", id, ok)
	}
}

func TestPickReturnsTierAfterExpiry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tiers := []Tier{
		{ID: "a", Idx: 0, Enabled: true},
		{ID: "b", Idx: 1, Enabled: true},
	}
	cooling := map[string]time.Time{"a": now}
	if id, ok := Pick(tiers, cooling, now); !ok || id != "a" {
		t.Fatalf("expired cooldown must return a: got %q,%v", id, ok)
	}
}

func TestPickQueuesWhenAllCooling(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tiers := []Tier{
		{ID: "a", Idx: 0, Enabled: true},
		{ID: "b", Idx: 1, Enabled: false},
	}
	cooling := map[string]time.Time{"a": now.Add(time.Minute)}
	if id, ok := Pick(tiers, cooling, now); ok || id != "" {
		t.Fatalf("want queued got %q,%v", id, ok)
	}
}
