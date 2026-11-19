package claim

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Tier struct {
	ID        string
	Idx       int
	RuntimeID string
	Enabled   bool
}

const defaultCooldown = 15 * time.Minute

func ParseCooldown(now time.Time, retryAfter string) time.Time {
	v := strings.TrimSpace(retryAfter)
	if v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return now.Add(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(v); err == nil {
			return t
		}
	}
	return now.Add(defaultCooldown)
}

func Pick(tiers []Tier, cooling map[string]time.Time, now time.Time) (string, bool) {
	var best Tier
	found := false
	for _, t := range tiers {
		if !t.Enabled {
			continue
		}
		if until, ok := cooling[t.ID]; ok && until.After(now) {
			continue
		}
		if !found || t.Idx < best.Idx || (t.Idx == best.Idx && t.ID < best.ID) {
			best = t
			found = true
		}
	}
	if !found {
		return "", false
	}
	return best.ID, true
}
