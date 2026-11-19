# Task G2 — tier-stamped claim (Go)

Work in the current directory. Write exactly ONE file, `claim.go`, in Go 1.23, `package claim`. Standard library only, no other files, no tests of your own, no network.

Context: after M-351 a queued task row can be *stamped* with the tier it was resolved to at enqueue time. Claiming has to accept a stamped task on any runtime that hosts that tier, while a legacy (unstamped) task may only be claimed by the runtime it was enqueued for. A stamped task naming a tier nobody hosts must be refused, never silently fall back to the legacy rule.

## Contract (exact — the grader compiles against these declarations)

```go
type Task struct {
	ID        string
	RuntimeID string // runtime the task was enqueued for; may be empty when stamped-and-parked
	TierID    string // "" means unstamped (legacy)
}

// Claimable reports whether runtimeID may claim task right now.
// tierRuntimes maps tier ID -> runtime IDs that host that tier.
func Claimable(task Task, runtimeID string, tierRuntimes map[string][]string) bool
```

## Rules

1. `TierID == ""` (legacy): claimable only when `runtimeID == task.RuntimeID` and both are non-empty.
2. `TierID != ""` (stamped): claimable only when `tierRuntimes[TierID]` contains `runtimeID`.
3. Stamped with an unknown tier, or a tier with no hosts, is never claimable.
4. Never claim with an empty `runtimeID`.

Write the file, then stop.
