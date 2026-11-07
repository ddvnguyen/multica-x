# Answering a workflow handoff in Decisions (M-274)

An issue in `wait_for_decision` is waiting on a human. The member answers it
from **Decisions → Pending**, without opening the issue: the form shows the
agent's question, its choices, a free-text answer and the status the issue moves
to, and submits through the `POST /issues/{id}/decision-response` transaction
that already existed (M-209, `server/internal/handler/issue_decision_response.go`).

Two reads make that possible, and both are the client's:

| Field | Source |
|---|---|
| The question | The last comment an **agent** wrote on the issue (`GET /issues/{id}/timeline`) |
| The choices | An optional `decision` block in that comment (below) |
| Default destination | The latest `status_changed` activity whose `to` is `wait_for_decision` — the status the issue left |
| Legal destinations | The issue's workflow steps, minus the waiting step the server refuses |

With no block, the form still answers: the choices fall back to **Approve /
Request changes / Other** and the member names the destination.

## The `decision` block

A grilling comment (the `wait_for_decision` handoff comment) may carry one
fenced block. Everything around it is prose the member reads; the block itself
is stripped from that prose and rendered as the option list.

````markdown
I need a direction before I touch the schema.

```decision
question: Which way should the migration go?
options:
  - key: expand
    label: Expand, then contract
    detail: Two deploys, no downtime
    next_status: in_progress
  - key: bigbang
    label: Big-bang cutover
    detail: One deploy, some downtime
default: expand
```

Anything else and I stay parked.
````

- `question` — one line, rendered above the choices. Optional; the prose is the
  question when it is absent.
- `options` — an ordered list. Each entry needs a `label` (the short choice);
  `key`, `detail` and `next_status` are optional. A bullet with no `key` gets
  its position as one, and a bullet that is bare text is a label.
- `default` — the option key (or its label) to preselect.

Parsing is deliberately lenient (`packages/core/decisions/handoff.ts`): unknown
fields are ignored, quotes are stripped, and a block whose options do not parse
is treated as no block at all — the card never loses its form over a typo.

## What the answer sends

`useRespondDecision` (`packages/core/decisions/mutations.ts`) posts:

| Field | Value |
|---|---|
| `client_request_id` | Minted per intent and reused on retry, so a dropped response resolves to the receipt the server already wrote; retired on a 409 |
| `expected_issue_revision` | The queue row's `issue_revision` — the state the member was reading |
| `decision` | The chosen option's **label**, or `custom` when only text was written |
| `rationale` | The free-text box, whether or not an option was picked |
| `next_status` | The selected destination; an option's own `next_status` preselects it and the member can change it |

A 409 means the row on screen is stale (the issue moved, or another member
answered): the form re-reads the issue and says so instead of retrying blindly.
A 400 is the server naming what it refused (a destination outside the workflow,
a destination that does not leave the waiting step) and is shown as given.

## Writing one as an agent

Post the block in the same comment that asks the question, then move the issue
to `wait_for_decision`. Keep `options[].next_status` on the steps the workflow
actually has, and leave it off when the member should choose — the default
destination is where the issue came from, which is right for "approve the plan
and continue".

The contract for the Leader: **every `wait_for_decision` handoff comment carries
a `decision` block**; it is what makes the handoff answerable in one click.
