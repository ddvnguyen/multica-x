# Task G3 — provider 429 cooldown (Go)

Work in the current directory. Write exactly ONE file, `cooldown.go`, in Go 1.23, `package claim`. Standard library only, no other files, no tests of your own, no network.

Context: when a provider answers 429, the tier that hit it must cool down before it is tried again. The `Retry-After` header is the only source of the duration; when it is missing or unusable the cooldown is 15 minutes.

## Contract (exact — the grader compiles against these declarations)

```go
type Tier struct {
	ID        string
	Idx       int
	RuntimeID string
	Enabled   bool
}

// ParseCooldown turns a Retry-After header value into the instant the tier may
// be used again. retryAfter may be delta-seconds ("120") or an HTTP-date
// (RFC1123). Anything missing, negative or unparseable means now + 15 minutes.
func ParseCooldown(now time.Time, retryAfter string) time.Time

// Pick returns the ID of the enabled tier with the smallest Idx that is not
// cooling down at now (cooling[tierID] is after now). ok == false means every
// enabled tier is cooling (or there are none): the task stays queued.
func Pick(tiers []Tier, cooling map[string]time.Time, now time.Time) (id string, ok bool)
```

`ParseCooldown` imports `time`; `Pick` must treat a tier with no entry in `cooling` as available, and a tier whose cooldown equals `now` exactly as available.

Write the file, then stop.
