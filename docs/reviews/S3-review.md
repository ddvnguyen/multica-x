# S3 review: MMX harness runtime (merged code)

Read-only review of the S3 code already merged to `multica-moct`, tracked by
[ddvnguyen/multica-x#87](https://github.com/ddvnguyen/multica-x/issues/87).
No code or config was changed.

| Repo | Ref reviewed |
|---|---|
| `ddvnguyen/multica-x` | `multica-moct` @ `93b6f61` |
| `ddvnguyen/dsh-multica-runtime` (bridge) | `multica-moct` @ `d25bc47` |
| `ddvnguyen/deepseek-harness` | `integration` @ `63b27da0`, PR #17 head `34ee6450` (context only) |

## Reading order (planned before reading)

1. Diffstats of all seven PRs, to place the files.
2. Migration 602 and its 590 → 602 renumbering, since it is the only schema change.
3. Steer/queue/stop end to end: `handler/task_supplement.go` → `queries/supplement.sql`
   → `daemon/task_supplement.go` → `pkg/agent/dsh.go` → bridge `src/index.ts` and `src/protocol.ts`.
4. Summary clock: `daemon/dsh_summary.go` → `pkg/agent/dsh.go` `SummarizeSession`
   → bridge `summarize()` → sidecar → `handler/task_lifecycle.go`.
5. Seeded start: `daemon/dsh_session_start.go` → `daemon/daemon.go` run setup
   → `handler/daemon.go` linked lookup → `pkg/agent/dsh.go` fork → bridge fork.
6. Detached runtime: `pkg/agent/dsh_detached.go`, `daemon/config.go`,
   `cmd/server/runtime_sweeper.go`, `queries/agent.sql`.
7. Cross-check every flow against the "decisions already made" in the issue.

Haiku sub-agents traced each flow and reported with `path:line` references.
Every finding ranked High below was re-read at the cited lines before being
written here. Lines are at the reviewed refs.

## Verdict per PR

| PR | Scope | Verdict |
|---|---|---|
| multica-x #32 | steer/queue/stop via `task_supplement`, migration 602 | **Ship with fixes**: F1, F7 |
| multica-x #46 | summary + cache clock | **Ship with fixes**: F4, F5, F9, F10. Two contradict the decisions |
| multica-x #45 | detached-runtime restart grace | **Ship (dark) as is; do not enable** `MULTICA_DETACHED_RUNTIME` until F6 and F11–F13 are fixed |
| multica-x #68 | seeded session start | **Ship with fixes**: F2 and F3 are correctness bugs in the decision path |
| bridge #1 | steer/queue commands, `inbox_state` | **Ship with fixes**: F7, F14 |
| bridge #2 | summarize + capability | **Ship with fixes**: F5, F8, F15 |
| bridge #3 | fork a seeded session | **Ship with fixes**: F16, F17 |

None of the code is unsafe to keep merged. The detached path is off by default,
and the other defects degrade to "fresh start", "message dropped", or "row
mislabelled" rather than data loss on the server. F1–F3, however, break
behaviour the S3 design promises.

## Findings, ranked

Confidence: **Confirmed (read)** means re-read at the cited lines.
**Confirmed (test)** means shown by a test run. **Plausible** means traced by
a sub-agent but depends on code outside these repos (dsh internals) or was not
re-read.

### High

**F1. A steer/queue the bridge never acknowledges is recorded as delivered.**
`server/pkg/agent/dsh.go:979-1008` (`waitForDshInboxState`).
- Scenario: the bridge is busy, crashed mid-write, or predates `inbox_state` but
  advertises `steer`. The 2 s timer fires and returns `nil` (`:1005-1006`).
  `daemon/task_supplement.go:178-183` then acks `delivered=true`. Any state other
  than `rejected` is also success (`:1000-1001`).
- Also, `accepted` counts as delivered, but the bridge applies a message only at
  the next `step/start` or `turn/start` (F7). The user sees "delivered" for a
  message that may never reach the model.
- Fix (Sonnet): on timeout, return a typed `errDshInboxUnconfirmed` and ack it as
  failed with a stable reason (`inbox_unconfirmed`), so it is retryable through
  the existing `RetryTaskSupplement`. Add a test with a fake bridge that never
  sends `inbox_state`.
- Confidence: Confirmed (read).

**F2. A warm *linked* session is recorded as "resumed" but the run starts fresh, with no seed.**
`server/internal/daemon/daemon.go:8692-8730`, `:9052`;
`server/internal/handler/daemon.go:3560-3567`.
- Scenario: issue B has no session of its own, and its parent issue A has a dsh
  session that ended 10 min ago (warm). The handler fills only
  `LinkedPriorSession*`. `decideDshSessionStart` returns verdict `resumed` with
  `plan.ResumeSessionID = <A's session>`. The daemon never reads
  `plan.ResumeSessionID` (only `dsh_session_start.go:156,188` and the test do).
  Execute uses `ResumeSessionID: task.PriorSessionID` (`daemon.go:9052`), which is
  `""`. Result: a cold, unseeded start, with the run row saying `resumed`.
- The test table at `dsh_session_start_test.go:132-143` expects the linked resume
  but only checks the plan, never the exec options.
- Fix (Sonnet): when the verdict is `resumed`, set
  `task.PriorSessionID = plan.ResumeSessionID` and
  `task.PriorWorkDir = plan.Candidate.WorkDir`, before the reachability gate.
  Add a daemon-level test asserting `execOpts.ResumeSessionID`.
- Confidence: Confirmed (read).

**F3. The session-start verdict is computed before the resume-reachability gate.**
`server/internal/daemon/daemon.go:8692-8730` (verdict, recorded via `defer`)
versus `:8736` (`gateResumeToReachableSession`, which clears
`task.PriorSessionID` at `:6712-6720`).
- Scenario: a warm same-issue session whose `PriorWorkDir` is gone (worktree
  GC'd, or a different host). The verdict is `resumed`, the gate clears the
  resume, the run starts fresh without a seed, and the row says `resumed`.
  Because the gate runs after the decision, the summary seed that existed is
  also skipped.
- Fix (Sonnet): run the reachability check on the candidate inside
  `decideDshSessionStart` (inject a `Reachable func(candidate) bool`), so that an
  unreachable warm candidate falls through to the seeded/fresh branch. Add a
  table case.
- Confidence: Confirmed (read).

**F4. The summary trigger measures growth within one run, not growth above the init prefix (contradicts decision).**
`server/internal/daemon/dsh_summary.go:349-355`; facts from `pkg/agent/dsh.go:798-805`.
- Decision: "trigger = growth of about 20K tokens above the init prefix."
- Code: `tokensAboveInit := LastPromptTokens - FirstPromptTokens` of *this run*.
  On a resumed run, the first request already carries the whole history.
- Scenario: a session resumed five times, each run adding 6K tokens, is 30K
  above its init prefix but never reaches 20K in any single run, so it is never
  summarized. It then goes cold and starts fresh, losing all context. The
  comment at `:349` and `CLI_AND_DAEMON.md:360-363` describe the intended
  behaviour, not the actual one.
- Fix (Sonnet): persist the session's init-prefix size (the first run's
  `FirstPromptTokens`) beside the session (sidecar or run row), and compute
  `LastPromptTokens - initPrefix`. Add a test with two resumed runs.
- Confidence: Confirmed (read).

**F5. The summary size target (1–2K) and cap (4K) are not enforced anywhere (contradicts decision).**
`server/internal/daemon/dsh_summary.go:405-408` (cap only logs);
`server/pkg/agent/dsh.go:113-120` (`dshSummarizeCommand` has no budget field);
bridge `src/index.ts:836-858` (no limit, whatever `compactNow` returns).
- Scenario: compaction returns a 9K-token summary. It is stored untruncated and
  then used verbatim as the seed of the next session, which goes over the cap
  that the seed decision was sized around.
- Fix (Sonnet, two repos): add `max_tokens` to the summarize command and the
  bridge protocol, and pass it to compaction if it supports a budget. On the
  daemon, reject (`summary-over-cap`, fall back to fresh) or truncate at the cap
  before writing the sidecar. Test both sides.
- Confidence: Confirmed (read). Whether `dsh-compaction` accepts a budget is
  Plausible (package not inspected).

### Medium

**F6. Detached runtime: the daemon and the launcher parse `MULTICA_DETACHED_RUNTIME` differently.**
`server/internal/daemon/config.go:658` (`boolFromEnv`: true/1/yes/on, Linux only)
versus `server/pkg/agent/dsh_detached.go:79-95` (`== "1"` plus `systemd-run` on PATH).
- Scenario: an operator sets `MULTICA_DETACHED_RUNTIME=true`, or sets `=1` on a
  host without `systemd-run`. The daemon advertises `detached-runtime-v1`, but
  runs launch attached. The server then skips orphan recovery for that runtime
  (`handler/task_lifecycle.go:35-50`), and a run orphaned by a daemon restart
  stays `running` until the detached timeout (~3.5 h by default).
- Not reachable while the flag stays unset (the decision), so Medium.
- Fix (Sonnet): one function in `pkg/agent` (`DshDetachedAvailable()`) used by
  both the config and the launcher, including the `systemd-run` probe. Add a
  test for `true` and for a missing `systemd-run`.
- Confidence: Confirmed (read).

**F7. Queue/steer entries have no terminal state when the run ends, so accepted messages can be lost silently.**
Bridge `src/index.ts:146-163`, `:202-225`, `:240-252`; `:1019` (`finished` set
only after `execute()` returns); Go side F1.
- Scenario: the user queues "also update the changelog" while the agent is on
  its last step. The bridge accepts it (no busy check), and Go acks it delivered.
  The run reaches result, and no further `turn/start` occurs, so the entry is
  never applied or rejected. Entries accepted during the quiescence/flush/dispose
  window (`:753-756`) and steers sent after `cancel` (no `cancelRequested` check)
  are also accepted.
- Fix (Sonnet, bridge): at result/cancel, emit `rejected` with reason
  `run_finished` for every pending entry, and reject new steer/queue once
  `cancelRequested` or once execute enters teardown. Go side: treat only
  `applied` as delivered, or keep `accepted` and settle on the run's terminal
  report. Pick one and document it in `protocol.ts`.
- Confidence: Confirmed (read) for missing terminal states. Plausible on whether
  dsh itself drains followups after the final turn.

**F8. The bridge advertises `summarize: true` unconditionally.**
Bridge `src/index.ts:987-1001` versus `:804-816`.
- Scenario: a dsh profile without `dsh-compaction`. Go sees the capability, runs
  the summarize, and records `summarize-failed` on every cold session instead of
  `skipped-bridge-unsupported`. That pollutes run rows and spawns one process
  per session per TTL.
- Fix (Sonnet, bridge): set `summarize: ctx.get('compaction') !== undefined`,
  and test the ready frame.
- Confidence: Confirmed (read).

**F9. Summarize compacts the parent session in place.**
Bridge `src/index.ts:795-799`, `:836-849`.
- The design treats the parent as the read-only source linked by
  `parentSession`. The bridge rewrites its history to the summary node and
  flushes. Any later *resume* of that parent reads only the summary. That
  happens on the unknown-`EndedAt` fallback (`dsh_session_start.go:164-167`, which
  defaults to `resumed`) and on F2/F3 once fixed.
- Not data loss on the server, but the full transcript of the parent is no
  longer resumable after summarize.
- Fix: decide whether that is intended. If not, have the bridge compute the
  summary without committing it (`compactNow` on a fork or a dry run, if dsh
  supports it). Owner decision; flagged, not re-decided.
- Confidence: Confirmed (read) for the bridge. Plausible for dsh's flush semantics.

**F10. Summary clock config: unsafe values are accepted silently.**
`server/internal/daemon/dsh_summary.go:87-97`, `:299-304`, `:489-539`; `helpers.go:21-31,61-70`.
- A negative `MULTICA_DSH_SUMMARY_MARGIN` makes the delay `TTL + |margin|`, so
  the summary fires after the cache is cold.
- A short TTL (`5m`) below the default 15 m margin never arms (Warn per run).
- A typo in `MULTICA_DSH_SUMMARY_ENABLED` silently keeps the clock on.
- Fix (Sonnet): validate at `LoadConfig` (margin ≥ 0, margin < TTL for every
  model, unknown boolean values rejected) and fail startup with a clear message.
  Add a table test.
- Confidence: Confirmed (read) for the margin expression. The other two are
  Plausible (not re-read).

**F11. Detached: no re-attach exists, so the restart "grace" only delays the failure.**
`server/pkg/agent/dsh_detached.go:189-200` (`exitStatus`, no non-test caller);
`dsh.go:446-447`.
- Scenario (flag on): the daemon restarts mid-run. The scope keeps running and
  writes `stdout.jsonl` and `exit`, but nothing reads them. The task stays
  `running` until the sweeper's detached timeout, then fails with `timeout`,
  even if the run finished successfully. A server-side cancel during the outage
  cannot stop the scope (`stopScope` only runs in the in-process cancel
  goroutine).
- Consistent with the decision to ship off. This is the reason the mid-run
  restart test cannot pass yet.
- Fix: a later card (re-attach on daemon start: scan `.multica-detached/*`, tail
  stdout, report the result). Larger than a single Sonnet card. Split into
  "read exit and report" and "re-attach a live scope".
- Confidence: Confirmed (read).

**F12. Detached: MCP auth headers and env are persisted to disk and never deleted.**
`server/pkg/agent/dsh.go:49-59,75,310-316,399`; `dsh_detached.go:119,163`.
- The execute frame, including `mcp_servers[].headers` (Authorization headers
  built at `dsh.go:310-316`) and `env`, is written to
  `<workdir>/.multica-detached/<task>/stdin.jsonl` (0600) and kept after the
  run. Workdirs are reused, uploaded into agent context, and readable by the
  agent itself.
- Fix (Sonnet): delete `stdin.jsonl` once the child has read the frame (or on
  `Close`), or pass the frame through an inherited fd or pipe and never write it
  to disk. Add a test asserting the file is gone after start.
- Confidence: Confirmed (read). Off by default.

**F13. Detached: the sweeper timeout fails healthy, live runs.**
`server/pkg/db/queries/agent.sql:1418-1428`; `cmd/server/runtime_sweeper.go:87-99`; `cmd/server/main.go:770-778`.
- The detached branch fails any `running` task whose `started_at` is older than
  the bound (default 9000 s + 30 min), and ignores the heartbeat. A healthy 4 h
  run on a detached runtime is failed while the daemon is up, and nothing stops
  its process. The "0 disables" behaviour is unreachable from env (`main.go`
  maps ≤0 to the default).
- Fix (Sonnet): apply the detached bound only when the runtime heartbeat is
  stale (mirror the legacy branch), and honour `0` as disabled. Extend
  `runtime_sweeper_detached_test.go`.
- Confidence: Confirmed (read).

### Low

**F14. Bridge: stdin EOF mid-run does nothing; the turn keeps running after the daemon disconnects.**
Bridge `src/index.ts:976-980`. Fix: on EOF with an active run, cancel it.
Plausible: Go kills the process group on cancel anyway (`dsh.go:478-500`), so
this matters only when the daemon dies without killing its children.

**F15. Bridge `summarize` resumes `session_id` without the cwd check that execute's resume does.**
Bridge `src/index.ts:823-833` versus `:632-635`. The Go side also spawns
summarize without `cmd.Dir` (`pkg/agent/dsh.go:1095-1097`), while the sidecar is
keyed by `ProjectKey(WorkDir)`. Fix: pass the work dir through and set
`cmd.Dir`. Plausible (dsh session lookup by cwd not verified).

**F16. Bridge fork: any `create` error is treated as a seed rejection and retried as a resume.**
Bridge `src/index.ts:605-611`. An MCP startup failure or a model error retries
via resume, fails again, and is reported as `DSH_RESUME_REJECTED`. Fix: fall
back only on a typed seed/fork error, and rethrow the rest. Add tests for a
missing parent and an MCP failure.

**F17. Session IDs `.` and `..` pass validation on both sides.**
`server/pkg/dshsession/dshsession.go:51-56` (`^[A-Za-z0-9._-]+$`); bridge
`protocol.ts:409,418-422,449-450` (non-empty only). A session id of `..` from
the bridge makes the sidecar write go to `<root>/<project>/../summary.json`
(`dsh_summary.go:159-186`). The id comes from our own bridge, so Low. Fix:
reject ids that are all dots and require a leading alphanumeric, on both sides.
Confirmed (read).

**F18. `DshForkUsed` is set before the fork succeeds.**
`server/pkg/agent/dsh.go:593-613` (`forkUsed` true at `:602`, cleared only on a
resumed session frame). A fork that errors before any `session` frame leaves the
row saying "seeded from summary". Fix: set it on the first `session` frame
whose id differs from the parent. Confirmed (read).

**F19. Warm/cold TTL uses the new run's model, while the clock armed with the prior run's model.**
`daemon.go:8699` versus `:9313` and `dsh_summary.go:299`. This only differs when
per-model `MULTICA_DSH_PROMPT_CACHE` entries exist and the agent's model
changed. Fix: record the model in the sidecar or run row, and use the prior
model for the warm check. Plausible.

**F20. A fork leaves the parent's summary timer armed.**
`daemon.go:8721-8726` clears `task.PriorSessionID` before the `Cancel` at
`:9001-9003`, so the parent is still summarized later for nothing (one extra
model call per fork). Fix: cancel by `plan.Candidate.SessionID`. Plausible.

**F21. Summary timers are lost on daemon restart.**
`dsh_summary.go` (in-memory map only; one `Arm` call at `daemon.go:9313`).
Documented as deferred (`CLI_AND_DAEMON.md:368-370`). After a restart, every
session idle at that moment goes cold without a summary and starts fresh.
Fix: on start, re-arm from recent terminal tasks (server query) or from the
sidecar store. Medium-sized card.

**F22. Unbounded request bodies on two daemon-auth routes.**
`handler/task_supplement.go:429` (ack) and `handler/task_lifecycle.go:212`
(summary decision) decode without `http.MaxBytesReader`, unlike `:233`. The
routes are daemon-authenticated, so Low. Fix: wrap with the existing cap.

**F23. Misleading "not capable" errors in `dshSupplement`.**
`pkg/agent/dsh.go:936-943`: when steer is missing but questions exist, the error
says "no question is pending". Cosmetic, but it lands in failure diagnostics.

### Checked and found sound

- **Migration 602.** `ADD COLUMN ... NOT NULL DEFAULT 'steer'` is metadata-only
  on PG11+. The CHECK is added without `NOT VALID`, so it scans `task_supplement`
  under an exclusive lock. That table is small, so this is acceptable. The
  590 → 602 renumbering from the v0.7.1 sync is gated by
  `whenRenamedObjectsAllPresent(objects602TaskSupplementMode)`
  (`cmd/migrate/main.go:496`, `migrate_fork_rename_conditions.go:166-168`), so
  lane databases that ran the old 590 do not re-run it. Down is reversible but
  turns queued rows into steer.
- **Supplement claim.** `ClaimNextTaskSupplement` is one statement with
  `FOR UPDATE OF s SKIP LOCKED` and an UPDATE to `delivering`, so no double
  claim. Ack comes after injection, so a lost ack leaves the row `delivering`
  (no duplicate). It is settled when the task ends.
- **N instances.** Supplements are keyed by task id, and the daemon delivers
  serially per task. `ClaimAgentTask` serializes per (agent, issue), so two
  tasks cannot resume the same session. Two tasks may fork the same parent;
  each gets a new id, so this is safe.
- **Capability gating** is layered: server capability row, daemon negotiation,
  bridge ready flags. Queue is rejected for non-dsh providers at the handler.
  Legacy runtimes are untouched (`_ = mode` in Claude/Cursor/Codex).
- **Detached default-off** holds. With the flag unset, nothing launches detached
  and the server's new branches are inert.
- **Secret logging.** No tokens, env, prompts, seeds or summaries are logged in
  the reviewed paths. Residual risk: provider/bridge stderr tails flow into
  `record.Error` on summarize failure and into the daemon log (F15 card can
  redact).
- **Wire compatibility.** The Go `inbox_state` parsing matches the bridge's
  frame (`command` + `state`).

### Decision contradictions (summary)

| Decision | Status |
|---|---|
| One session per task, N instances, per-session steer/queue/stop | Holds. Stop is task cancel → cancel frame → SIGTERM/SIGKILL. |
| Resume only while warm; summarize at TTL−margin; seed by `parentSession`; else fresh | Holds in the decision function, **broken in wiring** (F2, F3). Unknown `EndedAt` defaults to `resumed` (`dsh_session_start.go:164-167`), which is a warm assumption without evidence. |
| Summary 1–2K, cap 4K | **Not implemented** (F5). |
| Trigger ≈ 20K above init prefix | **Measured per run** (F4). |
| Sessions in the lane's shared dsh store, not `MULTICA_DSH_SESSION_ROOT` | **Only when `MESSAGE_SINK=dsh`.** The default is the private per-runtime/agent root (`dsh_session_start.go:368-382`, `daemon.go:8929-8931`). If lane config sets `MESSAGE_SINK=dsh`, this holds. That config is lane z and was not checked. Recommended: keep the code and confirm the lane env; alternatively log a Warn when dsh runs on the private root. |
| Detached grace ships off | Holds (F6 matters once it is turned on). |
| Divergence additive and capability-gated; legacy runtimes stay | Holds, except F8 (summarize capability overstated). |

## Tests run

| Command | Result |
|---|---|
| `go test ./internal/daemon/... ./pkg/agent/... ./cmd/server/... ./internal/handler/... -count=1` (Go 1.26.9, offline) | `internal/daemon`, `initctx`, `cmd/server`, `internal/handler`: **ok**. `internal/handler` ran in 0.015 s, so DB-backed tests skipped (no Postgres in the sandbox). |
| same | **FAIL** in packages S3 did not change: `pkg/agent` (6 Codex tests: timing bounds and retry counts), `daemon/execenv` (2), `daemon/processtree` (1, timing), `daemon/repocache` (4, git/CA environment). The only S3 touch in these packages is the `mode` parameter added to Codex's supplement signature (PR #32), which does not affect the failing paths. Judged environmental (sandbox timing and git config), **not verified against the base commit**. |
| All dsh tests in `pkg/agent` (`dsh_test`, `dsh_fork_test`, `dsh_detached_test`) and `internal/daemon` (`dsh_summary_test`, `dsh_session_start_test`) | **pass**. `dsh_detached_scope_test` skips without a user systemd manager. |
| Bridge `pnpm test` | **Not run.** The sandbox's permission policy blocked installing the bridge's npm dependencies. The bridge was reviewed by reading only. |

## Not reviewed

- dsh internals (`dsh-compaction`, the session store, followup semantics),
  which F7, F9, F15 and F17 partly depend on. The bridge `node_modules` were not installed.
- `deepseek-harness` PR #17 (live-follow external sessions, 1.3K lines, open):
  fetched for context, not reviewed line by line.
- Lane z: deploy config (`MESSAGE_SINK`, `DSH_HOME`, systemd units), acceptance
  runs, migrations against real databases.
- DB-backed Go tests (no Postgres available), and any end-to-end run with a real
  dsh.
- UI surfaces for supplement status.

## Proposed fix-up cards

Each card is sized for one Sonnet session. Cards in the same group touch
disjoint files and can run in parallel.

| Card | Findings | Files in scope | Group |
|---|---|---|---|
| **A. Inbox ack honesty (Go)** | F1, F18, F23 | `server/pkg/agent/dsh.go` (supplement and fork sections), `dsh_test.go`, `dsh_fork_test.go` | 1 |
| **B. Session-start wiring** | F2, F3, F19, F20 | `server/internal/daemon/daemon.go` (run setup), `dsh_session_start.go`, `dsh_session_start_test.go` | 1 |
| **C. Bridge inbox and capability** | F7 (bridge half), F8, F14, F16, F17 (bridge half) | bridge `src/index.ts`, `src/protocol.ts`, `tests/*` | 1 |
| **D. Detached hardening (before enabling)** | F6, F12, F13 | `server/pkg/agent/dsh_detached.go`, `server/internal/daemon/config.go`, `server/pkg/db/queries/agent.sql` (+ `make sqlc`), `server/cmd/server/runtime_sweeper.go`, `main.go`, tests | 1 |
| **E. Summary trigger and config** | F4, F10, F17 (Go half), F22 | `server/internal/daemon/dsh_summary.go`, `dsh_summary_test.go`, `server/pkg/dshsession/dshsession.go`, `server/internal/handler/task_lifecycle.go`, `task_supplement.go` | 1 |
| **F. Summary size budget (two repos)** | F5, F15 | `server/pkg/agent/dsh.go` (summarize section), bridge `src/index.ts` `summarize()`, `src/protocol.ts` | 2 (after A and C, same files) |
| **G. Restart continuity** | F11, F21 | new `server/internal/daemon/dsh_rearm.go`, `server/pkg/agent/dsh_detached.go` | 3 (after D and E) |
| **H. Owner decisions** | F9 (in-place compaction), shared-store default, unknown-`EndedAt` → resumed | none (decision note) | any |

Card A must settle the `accepted` versus `applied` contract with card C. Both
read the `inbox_state` comment in `protocol.ts` as the single spec.
