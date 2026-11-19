package claim

import "sort"

type Tier struct {
	ID        string
	Idx       int
	RuntimeID string
	Enabled   bool
}

type Slot struct {
	Online bool
	Used   int
	Slots  int
}

func Resolve(tiers []Tier, slots map[string]Slot) (string, bool) {
	cand := make([]Tier, 0, len(tiers))
	for _, t := range tiers {
		if !t.Enabled {
			continue
		}
		s, ok := slots[t.RuntimeID]
		if !ok || !s.Online || s.Used >= s.Slots {
			continue
		}
		cand = append(cand, t)
	}
	if len(cand) == 0 {
		return "", false
	}
	sort.Slice(cand, func(i, j int) bool {
		if cand[i].Idx != cand[j].Idx {
			return cand[i].Idx < cand[j].Idx
		}
		return cand[i].ID < cand[j].ID
	})
	return cand[0].ID, true
}
