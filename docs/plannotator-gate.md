# Plannotator gate

`server/internal/plannotator` wraps the [`plannotator`](https://docs.plannotator.ai)
CLI as the human-in-the-loop review step of the run loop: a plan or a diff is
rendered for a reviewer, and the review resumes the run.

## Why the process boundary, not the server

Plannotator starts a **temporary, unauthenticated HTTP server on a random local
port** for each session, and its documented local API is explicitly
session-scoped and version-unstable ("support the bundled browser UI… can change
with a Plannotator release"). There is no webhook for session lifecycle. Storing
a session URL as configuration is documented as wrong for exactly that reason.

So this package reads the verdict from the process and nothing else:

| Signal | Meaning |
| --- | --- |
| exit `0` | approved (feedback may still be attached) |
| exit `1` | annotated or dismissed — the JSON record says which |
| exit `2` | gate failure: no verdict exists |
| `--result-file` | the JSON record, authoritative when present |
| stdout | the JSON record, the fallback |

`ErrGateFailed` exists so a caller can never read "the gate broke" as "the
reviewer said no".

## Commands

Plan gate (the only mode that accepts `--require-approval` / `--result-file`):

```
plannotator annotate plan.md --gate --json --require-approval --result-file verdict.json
```

Diff gate (portable: a caller-supplied unified diff needs no git repository):

```
plannotator review --patch-file run.patch --gate --json
```

`--hook` is annotate-only, so a diff gate is always a wrapper process rather
than a host hook.

## Configuration

| Field | Environment | Use |
| --- | --- | --- |
| `Remote` | `PLANNOTATOR_REMOTE=1` | Headless daemon host: fixed port, `0.0.0.0` bind. |
| `Tailscale` | `--tailscale` flag | Serves the session over `tailscale serve`; the bind stays loopback. Preferred over `Remote` where available. |
| `Origin` | `PLANNOTATOR_ORIGIN` | Agent identity for feedback framing (`codex`, `dsh`, …). |
| `Browser` | `PLANNOTATOR_BROWSER` | `none` on a headless host: the URL is printed instead of launched. |
| `Port` | `PLANNOTATOR_PORT` | A port or range; fixed for remote mode. |

## Security

The review port is **unauthenticated**. Keep it on loopback, or reach it through
`--tailscale`, or put an authenticating proxy in front of it. Never expose it
publicly, and never proxy a daemon-supplied URL without an allow-list — the
plugin's `/moct` prefix
([moct-proxy.ts](../packages/dsh-plugin/src/moct-proxy.ts)) is the precedent
worth copying, not a general-purpose passthrough.

## Where this plugs into the loop

The daemon's park point (`reportInterruptedTask` →
`POST /daemon/tasks/{id}/blocked`) is where a run hands control to a human: the
`plan` tool submitted, the server parked the task `blocked`, and the session was
recorded for the resume. That point owns the plan id, so it is where a plan gate
belongs.

The gate reads the plan and returns the verdict over two task-scoped daemon
routes, because a daemon has a task token and no member session:

| Route | Purpose |
| --- | --- |
| `GET /api/daemon/tasks/{taskId}/parked-plan` | the open in-review plan's text, rendered as standalone Markdown |
| `POST /api/daemon/tasks/{taskId}/plan-verdict` | `{plan_id, base_revision, verdict, feedback}` |

Both resolve the plan **from the task**, so a daemon can reach exactly one plan —
the open one on its own task's issue — and can neither name nor guess another.
The verdict runs through `TaskService.ReviewPlan`, the same path the panel uses:
the revision fence, the first-verdict-wins CAS and the resume enqueue are
shared, and this seam adds no review logic and stores nothing.

Two consequences are deliberate:

- **The platform stays the system of record.** Revisions, comments, verdicts
  and history live in Multica's tables; Plannotator renders and collects. If the
  gate dies, the plan stays parked and the native panel
  ([plan-review-panel.tsx](../packages/views/issues/components/plan-review-panel.tsx))
  can still produce the verdict.
- **No new tables.** There is no `review_sessions` row and no reverse proxy:
  both would duplicate state the platform already owns.

## The daemon side

`reportInterruptedTask` is the park point: it reports the blocked task, then
calls `startPlanGate`, which spawns `runPlanGate` on its own goroutine. The
order matters — the park report is what makes the plan submittable, so a gate
that raced it would read "no plan" and drop a review the agent is already
waiting on. The goroutine matters too: the wait is a person, not an I/O bound,
and blocking the reporting path on a review would hold the claim loop.

```
MULTICA_PLAN_GATE=1        # opt-in; off by default
PLANNOTATOR_BIN=...        # executable; empty resolves `plannotator` on PATH
```

Off by default because the gate needs a binary *and* a person at this machine:
with neither, every park would only time out or deny.

Mapping, in `server/internal/daemon/plan_gate.go`:

| Gate | Verdict | Why |
| --- | --- | --- |
| `approved` | `approve` | feedback is dropped: nothing asked for changes |
| `annotated` | `changes_requested` + feedback as comments | |
| `dismissed` | *nothing* | closing a review is not a denial; the plan stays parked |
| exit `2` / no decision | *nothing* | `ErrGateFailed` never reads as a reviewer's no |
| `409` from the verdict route | logged, not retried | the panel or a cancel decided first |

The gate's stderr is forwarded to the daemon log and also captured (bounded to
8 KiB) for its first URL, logged as `plan gate: review session open`. That is
for the operator who cannot see the reviewer's browser; the verdict itself is
still read only from the process.

## Status

Delivered: the gate package, its argv/env contract, exit-code and record
parsing, the two daemon routes, and the daemon wiring that closes the loop from
park to verdict. A gate that cannot run leaves the plan parked and approveable
in the panel, so no failure here blocks a run.
