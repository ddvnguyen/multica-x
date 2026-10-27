# MOCT seam contract v1

The typed seam between `multica-be`/the Multica daemon and `moct-be` (M-97
rules 1–4). This document is the contract; the implementation lives in
`server/internal/moct/` (the only package in this repo that talks to
moct-be). Version pinned to moct `43960c5`, verified live on 2026-10-26 (see
[Live-check verdict](#live-check-verdict)).

## Identity split

**Multica owns execution identity (what runs); MOCT owns the knowledge
profile (what it knows).** A Multica agent is addressed inside MOCT only by
its ref string — MOCT never learns Multica's auth, roles or user table, and
Multica never reads MOCT's database. moct-be is the only writer to the
memory store; every call from this repo is an HTTP call through
`server/internal/moct/`.

## Ref convention (D2)

Every cross-reference is a prefixed string; moct-be matches them as exact
strings. Builders: `WorkspaceRef`, `ExpertRef`, `IssueRef`, `ProjectRef` in
`server/internal/moct/refs.go`.

| MOCT field | Value |
|---|---|
| `workspaceId` | `multica:ws:<uuid>` (Multica workspace) |
| `expertId` | `multica:agent:<uuid>` (agent's knowledge profile) |
| `taskRef` / tag `{kind:"task"}` / inject `taskRef` | `multica:issue:<uuid>` |
| tag `{kind:"channel"}` / inject `channelRef` | `multica:project:<uuid>` |
| `actor` on agent writes | `{kind:"member", id:"multica:agent:<uuid>"}` |
| `actor` on human writes (browser seam) | `{kind:"human", id:"multica:user:<uuid>"}` |

Rules learned from moct-be (live-verified):

- An issue ref goes in **home refs and scope params only**. A note's
  `taskRef` field accepts `task:…` refs exclusively — sending
  `multica:issue:…` there is a 400. `server/internal/moct.NoteParams`
  therefore has no `taskRef` field at all.
- No registration is required: inject, search and get accept any
  `multica:agent:*` string. There is no expert registry to maintain.
- `workspaceId` and `expertId` are **required** on every read (400 without).

## Configuration — one switch for both halves

`MOCT_BASE_URL` on the **daemon** (e.g. `http://127.0.0.1:8310`) turns the
seam on: the daemon then fetches the inject block on claim and adds the `moct`
MCP server to every task it runs. Unset — the default everywhere but a lane
that runs moct-be — nothing changes: no block, no MCP entry, no warning.The same value is forwarded to the stdio server as its `MOCT_BASE_URL`.

`MOCT_BASE_URL` says a host *can* reach moct-be. A workspace says whether it
*wants* memory: `workspace.settings.moct_memory_inject` (S2-C, M-123) turns the
pre-run block off for that workspace. The server reads it on every claim and
sends `moct_memory_inject` on the claim response; the daemon fetches the inject
block only when the field is absent (a server that predates the switch) or true.
Absent means ON, end to end: the switch only ever subtracts, and upgrading a
deployment must not strip memory from every workspace that never touched it. The
`moct` MCP tools are not affected — that is the agent's deliberate pull.

## Out-path 1 — pre-run inject

On claim, the daemon calls (`moctInjectPrompt` in
`server/internal/daemon/moct_memory.go`):

```
GET /team/v1/memory/inject?workspaceId=<multica:ws:…>&expertId=<multica:agent:…>
    &taskRef=<multica:issue:…>&channelRef=<multica:project:…>
```

Response: `{entries:[{id,type,title,body,attrs,home,priority,pinned,tokens}],
omitted, duplicates, conflicts, tokens, layerTokens, index}` — a
prompt-ready block, newest/highest priority first, capped by
`layerTokens` (default 1000). Only `status:"active"` records inject.

The daemon prepends the entries to the task prompt. Go type:
`moct.Inject` / `moct.Injection`; the prompt hook is `WithMoctInject`, and it
is the one run-scoped block that goes *ahead* of the task instruction rather
than behind it.

**Narrowing semantics (live):** the inject buckets match `home {kind,ref}`
exactly per bucket — task, channel, team, expert — so `taskRef` really does
scope the block to one issue. Omitting a ref skips that bucket.

## Out-path 2 — on-demand tools (`multica moct-mcp`)

A stdio MCP server named `moct`, added by the daemon to the task's merged MCP
config and merged by server name — the same overlay-wins rule as the per-task
overlay (`server/internal/handler/mcp_overlay.go:22`, implemented on the daemon
side by `mergeTaskRemoteMCPConfig`). The entry is `server/internal/moctmcp/`
served by the `multica moct-mcp` subcommand, built on `server/internal/moct/`.
The command is the running `multica` binary itself, so there is no second
artifact to install. Refs are passed as **env**, never as tool arguments — a
run cannot read or write outside its own scope:

| Env | Value |
|---|---|
| `MOCT_BASE_URL` | e.g. `http://127.0.0.1:8110` |
| `MOCT_WORKSPACE_ID` | `multica:ws:…` |
| `MOCT_EXPERT_ID` | `multica:agent:…` |
| `MOCT_TASK_REF` | `multica:issue:…` |
| `MOCT_CHANNEL_REF` | `multica:project:…` (optional) |

| Tool | Client method | Endpoint |
|---|---|---|
| `memory_search` | `moct.Search` | `GET /team/v1/memory` |
| `memory_get` | `moct.Get` | `POST /team/v1/memory/get` |
| `memory_note` | `moct.Note` | `POST /team/v1/memory` |

Tool-input contract:

- `memory_search`: `q`, `types`, `homes`, `status`, `limit`, `cursor`;
  scope comes from env.
- `memory_get`: `ids` (1–10); scope comes from env. Ids outside the scope
  come back in `missing`, never as data.
- `memory_note`: `type`, `title`, `body`, `attrs`, `home`;
  `workspaceId` and `actor` come from env. The default home for a run is
  the task home `{kind:"task", ref:$MOCT_TASK_REF}`.

Write semantics the tool must surface to the agent (live-verified):

- Near-duplicate titles hold the write: `record == null` plus
  `nearMatches` — resend with `confirmNew: true` to write anyway.
- Under moct-be's default `expertApproval:"required"`, a worker's
  `rule`/`decision`/`how_to_work` lands `status:"pending"` and does **not**
  inject until approved; the response `notice` says so. A `convention`
  activates immediately; `task`/`branch` writes are 403 for anyone who is
  not an orchestrator or the human.
- Types allowed at a task home: `decision` (and `task`, which Multica refs
  cannot satisfy — see above). A `decision` requires
  `attrs.decidedBy` and `attrs.sourceRef`.

## Out-path 3 — browser reads (`/moct` in the dsh plugin)

The dsh panel reads memory through its own host proxy, never moct-be: the
browser calls `/moct/...` on the dsh origin and
`packages/dsh-plugin/src/moct-proxy.ts` re-issues the request with the scope
the browser is not allowed to choose.

- Allow-list, and nothing else: `GET /team/v1/memory`,
  `POST /team/v1/memory/get`, `GET /team/v1/memory/:id/versions`,
  `GET /team/v1/memory/usage`, `POST /team/v1/memory/:id/{approve,retract}`,
  and a host-authored `/moct/status`. Notes, settings, inject, rule-feed and
  the other record verbs are a proxy-side 404.
- `workspaceId` is `multica:ws:<uuid>` resolved from `/api/workspaces` by the
  row's `workspaceSlug` (a slug that is not readable is a 503, never a fall
  back), `actor` is `{kind:"human", id:"multica:user:<uuid>"}` from
  `/api/me`, and an agent page addresses `expertId` only after the agent is
  confirmed in that workspace. `all=1` on reads.
- A by-id route reads the record back through a scoped `POST /memory/get`
  first; an id from another workspace is a 404 and never reaches a verb. A
  record with no workspace (`workspace_id IS NULL`) is readable but cannot be
  approved or retracted from one workspace.
- The acting human is the owner of the plugin's personal access token, so
  approvals are made **as that user**, and a token whose owner is not an
  owner/admin of the workspace is refused. A bridge that carries a live dsh
  session's own Multica identity would remove that limitation.

## Panel settings (S2-C, M-123)

Two settings the dsh panel's MOCT group reads and writes, both ordinary
multica-be routes behind the same session gate as everything else:

| Route | Who | Meaning |
|---|---|---|
| `GET /api/workspaces/{id}/moct` | member | `{memory_inject, memory_inject_set, editable}` |
| `PUT /api/workspaces/{id}/moct` | owner/admin | flips the pre-run switch above |
| `GET /api/workspaces/{id}/interactions` | member | the ask/plan wait, its bounds, `editable`, and every project override |
| `PUT /api/workspaces/{id}/interactions` | owner/admin | `{timeout_seconds}`; `null` clears back to the default |
| `PUT /api/projects/{id}/interactions` | owner/admin | `{timeout_seconds}`; `null` clears back to the workspace value |

The values live in the existing `workspace.settings` / `project.settings`
objects under `moct_memory_inject` and `ask_plan_timeout_seconds`, one key per
request (`SetWorkspaceSetting` / `SetProjectSetting`) so two admins editing
different keys cannot overwrite each other. `ask_plan_timeout_seconds` is
bounded to 60–86400 seconds and defaults to 600; it is **stored and surfaced
only** — no route here cancels a parked run or resumes one, and a cleared key
falls back to the compiled-in default rather than pinning today's value.

## Scope semantics (search / get)

Visibility = same workspace **AND** any one of: tag `team` = workspaceId,
tag `expert` = expertId, or a tag in `channels`/`tasks`. The tags are
**OR-ed**: adding `tasks=<issue>` widens, it does not narrow — every
`decision` carries a `team` tag and is therefore visible workspace-wide.
Per-issue narrowing in search comes from `q` keywords; per-issue narrowing
for prompt injection comes from inject's task bucket. `all=1` drops the tag
filter but keeps the workspace.

## Failure mode — memory never blocks a task

If moct-be is down, unreachable, times out (10s default) or answers 5xx,
the caller logs a warning and proceeds **without memory**:

- `errors.Is(err, moct.ErrUnavailable)` — transport failure / backend down.
- `errors.As(err, &apiErr)` — moct-be answered 4xx/5xx (`Code` +
  `Message` from its `{ok:false,code,error}` envelope).

Any error on the inject path degrades the same way; no error on this path
is ever fatal to a run. Client validation errors (missing scope, >10 ids)
are programming bugs and are returned as plain errors.

The stdio server follows the same rule from the other side: a moct-be that is
down, unconfigured or unparseable makes every tool call answer `isError:true`,
and `multica moct-mcp` stays alive. An ACP/dsh runtime fails a whole session
when a stdio MCP server exits (`server/pkg/agent/acp_session.go`), so exiting
would be exactly the "memory blocks a task" failure this contract forbids.

## Live-check verdict

Run 2026-10-26 against a loopback moct-be (`127.0.0.1:8110`, no auth,
scratch DB, moct `43960c5`); raw request/response evidence attached to
M-98. Results:

- **inject filters on `multica:*` refs** — positive and negative controls on
  task, channel and expert buckets all discriminated exactly;
- **search/get filter by exact tag match** with the OR-scope caveat above;
- **no registered expert required** for any read; missing
  `workspaceId`/`expertId` → 400;
- write constraints: `taskRef` must be `task:…` (400), `type=task`
  impossible for Multica refs (403 worker / 400 human), worker writes of
  rule/decision/how_to_work are `pending` under default settings,
  near-match dedupe needs `confirmNew`;
- **no moct-be schema change needed — no issue filed in `ddvnguyen/moct`.**
