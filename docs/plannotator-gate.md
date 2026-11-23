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

The verdict then maps onto the endpoints that already exist — `POST
/plans/{planId}/verdict` with the gate's feedback as comments — and
`TaskService.ReviewPlan` re-queues the resume run that continues the same
session. Two consequences are deliberate:

- **The platform stays the system of record.** Revisions, comments, verdicts
  and history live in Multica's tables; Plannotator renders and collects. If the
  gate dies, the plan stays parked and the native panel
  ([plan-review-panel.tsx](../packages/views/issues/components/plan-review-panel.tsx))
  can still produce the verdict.
- **No new tables.** There is no `review_sessions` row and no reverse proxy:
  both would duplicate state the platform already owns.

## Status

Delivered: the gate package, its argv/env contract, exit-code and record
parsing, and the tests that pin all three.

Not yet delivered: the daemon patch that spawns the gate at the park point and
posts the verdict. It needs the run's plan markdown, which lives on the server
(`GET /issues/{id}/ask-plan`) rather than on the daemon, so wiring it is a
server-surface change — a daemon-credentialed read of the open plan — rather
than a one-line spawn. Until then the gate is a library with no caller, and this
document says so instead of implying the loop is closed.
