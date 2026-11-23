# PowerContext memory seam

The second implementation of the daemon↔memory seam whose first is
[moct-seam.md](moct-seam.md). One run reads one memory plane, chosen by one
environment variable per host.

## The switch

| Host | Value | Effect |
| --- | --- | --- |
| Daemon | `POWERCONTEXT_BASE_URL` | Turns the seam on, e.g. `http://127.0.0.1:8000`. Empty (the default) changes nothing on the claim path. |
| Daemon | `POWERCONTEXT_TOKEN` | Bearer for a server with authentication enabled. Empty sends no `Authorization` header — correct for the default loopback-only install. |

`MOCT_BASE_URL` and `POWERCONTEXT_BASE_URL` are **mutually exclusive on one
host**, because one run gets one memory block, never two. With both set, MOCT
wins: it is the older, live-verified seam, and exporting a second variable must
not silently migrate a working lane. The run row's memory note names the
backend that answered, so a lane that "turned on PowerContext" and still sees
moct blocks can tell why.

What the variable turns on, and what it never touches:

- **On:** the pre-run push (below) and the `powercontext` MCP entry in every
  claimed task's config.
- **Off:** nothing else. The `moct` MCP entry, `ask`/`plan` tools, and every
  other overlay are unchanged, and `workspace.settings.moct_memory_inject`
  still subtracts the pre-run block for a workspace that does not want it.

## Out-path 1 — pre-run push

On claim, [`memoryInjectPrompt`](../server/internal/daemon/memory_context.go)
dispatches to `powerContextInjectPrompt`, which calls:

```
POST /v1/context/prepare
{"scope_id": "multica:project:<uuid>", "query": "<trigger comment, clamped>",
 "max_bytes": 24000}
```

The response envelope (`status`, `content`, `content_bytes`) is injected into
the same prompt slot MOCT uses, behind the same `PartMemory` report row. Two
rules keep the two backends comparable:

- **One budget.** `max_bytes` is `initctx.BudgetMemory * 4` — the inverse of the
  report's own `(bytes+3)/4` estimator — so a backend swap does not silently
  change how much context an agent gets.
- **Verbatim content.** PowerContext already applied the byte budget and
  embedded its citations; the daemon adds one heading and re-trims nothing.

## Scope mapping

PowerContext has **one flat Scope per read**. Multica has four identities, so
exactly one names the scope ([`refs.go`](../server/internal/powercontext/refs.go)):

| Multica | PowerContext | Why |
| --- | --- | --- |
| project | the Scope id (`multica:project:<uuid>`) | Project knowledge outlives an issue being closed. |
| workspace | fallback Scope (`multica:ws:<uuid>`) | A run with no project still needs somewhere to read. |
| issue | the handoff entry written at run end | A task never needs its own provisioned Scope. |
| agent | entry text and kind | Per-agent narrowing is an Access concern, not a scope string. |

The **strings stay the D2 refs** from [moct-seam.md](moct-seam.md) so both
backends answer to one vocabulary and a migration never rewrites stored refs.

## Out-path 2 — tools

PowerContext serves its own MCP endpoint at `/mcp`, so the daemon adds a
**remote** entry rather than a stdio wrapper:

```json
{"mcpServers": {"powercontext": {"type": "http", "url": "http://127.0.0.1:8000/mcp",
  "headers": {"Authorization": "Bearer …"}}}}
```

No second artifact to install, and no client to keep in step with the server.
The entry is merged by server name through the same
`mergeTaskRemoteMCPConfig` path as every other overlay.

## Step 13 — the handoff commit

At run end, [`powerContextRecordHandoff`](../server/internal/daemon/memory_context.go)
commits the outcome as one curated memory entry (`POST /v1/memory/remember`):
marker, issue/project/agent refs, session, status, branch, and a byte-bounded
copy of the run's report.

**Idempotency** is by the task id: the entry carries
`handoff:/multica/task/<task id>`, and the writer searches for that marker
before writing. A retried terminal callback or a daemon restart therefore cannot
make the next claim inject the same handoff twice.

## Failure contract

Inherited verbatim from the MOCT seam: **memory never blocks a task.**

| Situation | Behaviour |
| --- | --- |
| Backend unreachable | Run starts without memory; the row says "backend unreachable". |
| Scope unreadable (403/404/422) | Run starts without memory; the row names the HTTP status and the server's `code`, because an unreadable scope is a configuration fault, not an outage. |
| Nothing to say (`status: empty`) | No block; the row says which scope returned nothing. |
| Workspace switched it off | No block; the row says so. |
| No project or workspace identity | No block; the row says there is no scope to read. |
| Handoff write fails | Warning; the run's own result is unaffected. |

The client's 10 s timeout is the most a slow server can add to a claim.

## Not implemented here

- **Native handoff endpoints.** PowerContext's `/v1/handoff/*` and `/v1/work/*`
  are richer than a remembered entry: they carry revisions, acknowledgements
  and receivers. The seam writes through `/v1/memory/remember` because that
  payload is documented field-for-field, and a client built on guessed schemas
  would be a wire contract nobody verified. Wiring the native path is the
  natural next step once its request shapes are read from the served
  `/openapi.json`.
- **Per-tenant isolation.** The server's static bearer represents one local
  administrator and cannot model A/B users. A multi-tenant deployment needs
  injected Access bindings or per-task principals; a token suffix is not one.
- **Backend selection by measurement.** No hit@5/MRR comparison between
  PowerContext and moct-be has been run in this repository. Nothing in the code
  depends on one being better.

## Related

- [moct-seam.md](moct-seam.md) — the first implementation of this seam.
- [plannotator-gate.md](plannotator-gate.md) — the review gate that runs at the
  park point of the same loop.
