package claim

import "testing"

func g1slots(online map[string]bool, full map[string]bool) map[string]Slot {
	m := map[string]Slot{}
	for _, r := range []string{"r1", "r2", "r3", "r4"} {
		on, ok := online[r]
		if !ok {
			on = true
		}
		used := 0
		if full[r] {
			used = 2
		}
		m[r] = Slot{Online: on, Used: used, Slots: 2}
	}
	return m
}

func TestResolveLowestIdxEligible(t *testing.T) {
	tiers := []Tier{
		{ID: "c", Idx: 2, RuntimeID: "r3", Enabled: true},
		{ID: "a", Idx: 0, RuntimeID: "r1", Enabled: true},
		{ID: "b", Idx: 1, RuntimeID: "r2", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(nil, nil))
	if !ok || id != "a" {
		t.Fatalf("want a,true got %q,%v", id, ok)
	}
}

func TestResolveSkipsOfflineRuntime(t *testing.T) {
	tiers := []Tier{
		{ID: "a", Idx: 0, RuntimeID: "r1", Enabled: true},
		{ID: "b", Idx: 1, RuntimeID: "r2", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(map[string]bool{"r1": false}, nil))
	if !ok || id != "b" {
		t.Fatalf("offline tier must be skipped: want b,true got %q,%v", id, ok)
	}
}

func TestResolveSkipsFullRuntime(t *testing.T) {
	tiers := []Tier{
		{ID: "a", Idx: 0, RuntimeID: "r1", Enabled: true},
		{ID: "b", Idx: 1, RuntimeID: "r2", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(nil, map[string]bool{"r1": true}))
	if !ok || id != "b" {
		t.Fatalf("full tier must be skipped: want b,true got %q,%v", id, ok)
	}
}

func TestResolveQueuesWhenNothingUsable(t *testing.T) {
	tiers := []Tier{
		{ID: "a", Idx: 0, RuntimeID: "r1", Enabled: true},
		{ID: "b", Idx: 1, RuntimeID: "r2", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(map[string]bool{"r1": false, "r2": false}, nil))
	if ok || id != "" {
		t.Fatalf("want queued got %q,%v", id, ok)
	}
	if id, ok := Resolve(nil, g1slots(nil, nil)); ok || id != "" {
		t.Fatalf("empty tiers must queue, got %q,%v", id, ok)
	}
}

func TestResolveDisabledTierIgnored(t *testing.T) {
	tiers := []Tier{
		{ID: "a", Idx: 0, RuntimeID: "r1", Enabled: false},
		{ID: "b", Idx: 1, RuntimeID: "r2", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(nil, nil))
	if !ok || id != "b" {
		t.Fatalf("disabled tier must be skipped: want b,true got %q,%v", id, ok)
	}
}

func TestResolveTieBreaksByIDAscending(t *testing.T) {
	tiers := []Tier{
		{ID: "zz", Idx: 1, RuntimeID: "r2", Enabled: true},
		{ID: "aa", Idx: 1, RuntimeID: "r1", Enabled: true},
	}
	id, ok := Resolve(tiers, g1slots(nil, nil))
	if !ok || id != "aa" {
		t.Fatalf("tie must break by id ascending: want aa got %q", id)
	}
}
