# Ask/plan agent tools (M-119, S1-A2)

The `ask` and `plan` MCP tools every claimed issue task receives through the
per-task MCP overlay. They are **multica-be only** — this path never reaches
MOCT — and they are the delivery half of the S1-A1 ask/plan server contract
(`server/internal/handler/askplan.go`, PR #20; plan/design record: M-119).

## Pieces

| Piece | File |
|---|---|
| stdio MCP server (tools `ask`, `plan`) | `server/internal/askplanmcp/` |
| CLI subcommand `multica askplan-mcp` | `server/cmd/multica/cmd_askplan_mcp.go` |
| daemon overlay entry `multica` | `server/internal/daemon/askplan_tools.go`, injected in `daemon.go` beside the moct merge |
| HTTP edge the tools call | `server/internal/handler/askplan.go` (S1-A1, unchanged) |
| limits parity (schema vs server edge) | `TestAskPlanMCPToolLimitsMatchHandler` in `internal/handler/askplan_parity_test.go` |

## Environment contract

Like the moct entry, identity travels as **env, never tool arguments** — the
tool schemas do not carry workspace or task fields, so a run cannot express a
call outside its own task:

- `MULTICA_SERVER_URL` — normalized HTTP base of multica-be
- `MULTICA_TOKEN` — the run's task-scoped `mat_` token
- `MULTICA_WORKSPACE_ID`, `MULTICA_TASK_ID`

The server independently re-checks the token's task binding
(`callerRunningIssueTask`), so even a hand-crafted request can only park the
run that owns the token.

## Flow (non-blocking)

1. Agent calls `ask` (1–6 questions) or `plan` (`create` → `write` → `submit`).
2. The tool returns immediately on the server's 202; the server has parked
   the task as `blocked` ("awaiting human") and the daemon interrupted the
   run. The tool result may be cut short by that interrupt — by design; the
   resumed prompt is the reliable channel.
3. A member answers the card or verdicts the plan from the Inbox; the answer
   re-queues a run resuming the same session, rendering `[ANSWER]` /
   `[PLAN VERDICT]` in its prompt.

The member's surface is the island's ask/plan panel —
`packages/views/issues/components/ask-plan-panel.tsx` (M-120), mounted on the
issue page, which is also what the Inbox detail pane renders. It reads the one
S1-A1 island endpoint `GET /issues/{id}/ask-plan` and posts back to
`POST /question-cards/{cardId}/answer` and `POST /plans/{planId}/verdict`;
the dsh rail's **Multica Inbox** row carries an unread dot while either state
is waiting.

## Failure and degradation

A failed call is a **tool error**, never a protocol error and never an exit —
an MCP server that dies fails the whole ACP/dsh session
(`server/pkg/agent/acp_session.go`). Validation mirrors the server edge (the
parity test pins the numbers); non-2xx bodies (including `409
stale_plan_revision` with `current_revision`/`current_status`) pass through
verbatim so the model can self-correct on retry.

The daemon **skips the entry** (task runs without the tools, with a warning)
when: the task has no issue, the token is missing or not `mat_`, the URL is
unusable, the `multica` binary cannot be located, or the runtime cannot carry
MCP servers at all (`agent.SupportsTaskMCP` — builtin pi below its
MCP-extension floor of `piMcpMinVersion` would fail the launch on the entry).
