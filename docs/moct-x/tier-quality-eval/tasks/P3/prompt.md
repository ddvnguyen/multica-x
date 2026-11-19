# Task P3 — plan: provider 429 cooldown

A tier is `(runtime, model)` and can be rate-limited by its provider. When a run fails with HTTP 429 the tier must cool down and the next tier by `idx` must take the task; a later task may use the tier again once the cooldown expires.

Write a plan (≤400 words) with these headed sections only:

1. `## Outcome` — what changes for the user during a provider outage.
2. `## Failure classification` — a table or list mapping: 429 with `Retry-After`, 429 without it, 402/quota-exhausted, 5xx, auth error → action and cooldown duration.
3. `## Data path` — how the provider's 429 text and headers travel from the agent process to the server (name the field/message that carries them), and where the cooldown is stored.
4. `## Tests` — at least two, one asserting the fallback to `idx` 1 and one asserting expiry returns the task to `idx` 0.
5. `## Risks` — two risks with mitigations.

Plan only; write no code files.
