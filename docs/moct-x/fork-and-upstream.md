# Fork discipline and upstream merges

This repository is a **fork of `multica-ai/multica`** (source-available, so
the merge is a normal git merge, not a port). The fork exists to run Multica
inside dsh on top of MOCT; its job is to stay as close to upstream as that
allows.

## Bases of record

| | | Source |
|---|---|---|
| Upstream release | `multica-ai/multica` at `v0.6.5` | `packages/dsh-plugin/FORK.md` § Bases |
| Fork base commit | `cbd75011` — `feat(issues): collapse board columns… (M-24)`, full sha `cbd75011884bd0ee75e9de57d86fbb05c150c421` | `FORK.md`; full sha from the M-95 spike verdict on M-93 |
| Fork of record | this repo, branch **`multica-moct`** (all PRs target it) | `FORK.md`; `deploy/dsh-z/dsh-z.env` pins `MULTICA_X_REF=multica-moct` |

Two different questions, two numbers (per `FORK.md`): what the fork started
from (`v0.6.5`), and where the plugin's rendering was last known correct
(`cbd75011`).

> The `v0.6.5` label is recorded in `FORK.md` and comes from the M-95 spike;
> this repository carries no `v0.6.5` git tag (checked: no local/remote tag
> matches), so the label itself is not verifiable from git here.

## Where divergence is allowed

The M-97 decisions name the sanctioned hot spots; the current head's actual
divergence (`git diff cbd75011..HEAD`, 13 PRs) is:

| Area | What diverges | Size |
|---|---|---|
| `server/internal/moct/` | **new** — the only HTTP client to moct-be | 4 files + tests |
| `server/internal/moctmcp/` | **new** — `multica moct-mcp` stdio server | 2 files + test |
| `server/cmd/multica/` | new `cmd_moct_mcp.go` + registration in `main.go:66,91` | 2 files |
| `server/internal/daemon/` | `moct_memory.go` (new), hooks in `daemon.go:7965-7983,8483-8491`, `moct` prompt option in `prompt.go`, `MoctBaseURL` in `config.go` | 4 files + tests |
| `server/pkg/agent/` | dsh-runtime ask/plan frames: `dsh.go`, `agent.go` (`MessageQuestion`), `version.go` gate | 3 files + tests |
| `packages/dsh-plugin/` | **new package** — the entire dsh UI integration | ~everything |
| `packages/ui/` | `portal-container.tsx` (+test) and the overlay components routing through it, so portals render into the island's shadow root | ~300 lines total |
| `packages/views/`, `scripts/`, `turbo.json`, `.github/workflows/ci.yml`, `.env.example` | export/wildcard checks, CI scope for `multica-moct`, small knobs | small |
| `deploy/dsh-z/` | **new** — lane-z provisioner | 8 files |
| `docs/moct-seam.md`, `docs/moct-x/` | **new** — seam contract + these docs | — |

Everything else — the work-plane server, the client packages, the app shell —
is byte-identical to upstream.

The **handler** is deliberately *not* divergent: no file under
`server/internal/handler/` references moct (repo-wide grep), and the
server-side per-task MCP overlay (`mcp_overlay.go`) is upstream code the seam
reuses as-is.

## Rules that keep the fork mergeable

1. **One fork entry for the plugin.** All `@multica/*` consumption by the
   dsh plugin goes through `packages/dsh-plugin/src/fork.ts` (public exports
   only), enforced by `scripts/check-fork-entry.mjs` in `pnpm lint`
   (`packages/dsh-plugin/package.json:44`). See
   [dsh-plugin.md](dsh-plugin.md#the-single-fork-entry-and-its-lint).
2. **Additive and thin.** The seam is one new package plus two daemon hooks;
   it reuses upstream's MCP-overlay and prompt-option mechanisms instead of
   reworking them (`docs/moct-seam.md`, `server/internal/handler/mcp_overlay.go:22-25`).
3. **Hide, do not delete.** Surfaces the dsh panel must not show are hidden
   in the island, never removed from the fork
   (`packages/dsh-plugin/src/client/navigation.ts:18-24`) — so an upstream
   merge never has to reconcile deletions.
4. **Divergence markers.** New/changed fork files carry their issue id in a
   header comment (`M-98`, `M-101`, `M-106`, `M-109` markers, e.g.
   `server/internal/moct/client.go:1`, `server/internal/daemon/moct_memory.go:3`), which
   makes `git log` a complete divergence register.

## Merging upstream

Working procedure (derived from `FORK.md` § Updating the fork base and the
lane's release flow):

1. Fetch upstream and merge `multica-ai/multica` (or the next tag) into a
   throwaway branch off `multica-moct`; resolve conflicts **in favour of
   upstream everywhere except the hot spots above** — the hot spots are the
   files this fork owns.
2. Run the fork's contract tests:
   `pnpm --filter @multica/dsh-plugin test` (island renders board + issue
   against a stubbed backend — a changed provider or route contract fails
   there, `FORK.md`), plus the workspace build and the Go test suites that
   CI runs for `server/**`.
3. Check the seam contract for drift: `docs/moct-seam.md` must still match
   `server/internal/moct/` (endpoint paths, env names, limits).
4. PR into `multica-moct`; merge gate is Claude Architect approval + CI
   green (M-97 decision Q6).

Note: the fork does **not** track upstream's `main` continuously; syncs are
deliberate, phase-3 work in the M-97 phasing (recorded, not automated).

## Recording a new base

When upstream sync moves the rendering surface (`packages/core`,
`packages/ui`, `packages/views` exports that `src/fork.ts` depends on), the
base move and the island adaptation land in **the same PR**
(`packages/dsh-plugin/FORK.md` § Updating the fork base):

1. Update the fork-base sha in `FORK.md` and read the diff for anything the
   `src/fork.ts` exports depend on.
2. `pnpm --filter @multica/dsh-plugin test`.
3. `pnpm lint` in the package (the entry gate), then `pnpm -w build`.

`deploy/dsh-z/deploy.sh` builds releases keyed by commit sha from
`MULTICA_X_REPO@MULTICA_X_REF`, so the lane records exactly which head it
runs; moving `MULTICA_X_REF` is how a proving run pins a candidate.

## Unverified

- The `v0.6.5` upstream label (boxed note above).
- This document's merge procedure is the `FORK.md` + M-97 Q6 discipline as
  written; no upstream merge has been performed yet on this fork (the branch
  history contains only fork PRs, `git log cbd75011..HEAD`), so the procedure
  is untested end-to-end.
- dsh-x production deploys track `stable`/`master` per workspace convention;
  nothing in this repo implements or verifies that.
