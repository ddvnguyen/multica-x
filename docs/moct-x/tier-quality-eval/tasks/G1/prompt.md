# Task G1 — tier resolver (Go)

Work in the current directory. Write exactly ONE file, `resolve.go`, in Go 1.23, `package claim`. Standard library only, no other files, no tests of your own, no network.

Context: an agent task is enqueued and must be assigned to a model tier. Tiers are ordered: `Idx` 0 is tried first, 99 last. A tier is usable only when it is enabled AND its runtime is online AND that runtime has a free session slot (`Used < Slots`). If no tier is usable the task stays queued.

## Contract (exact — the grader compiles against these declarations)

```go
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

// Resolve returns the ID of the tier that should serve the task.
// ok == false means no tier is usable and the task must stay queued.
func Resolve(tiers []Tier, slots map[string]Slot) (id string, ok bool)
```

## Rules

1. Consider only tiers with `Enabled == true` whose `slots[RuntimeID]` entry exists, is `Online`, and has `Used < Slots`.
2. Among the eligible tiers choose the smallest `Idx`; break ties by `ID` ascending.
3. Input order must not matter.
4. An empty or fully ineligible slice returns `("", false)`.

Write the file, then stop.
