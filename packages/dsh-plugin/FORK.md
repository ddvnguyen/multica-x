# Fork provenance

This package renders the Multica board and issue page inside DeepSeek Harness.
What it renders is not a re-implementation: it is this repository, which is a
fork of Multica. This file records which fork, what it may consume, and how it
is expected to diverge.

## Bases

| | |
| --- | --- |
| Upstream | `multica-ai/multica` at `v0.6.5` |
| Fork base | `cbd75011` — `feat(issues): collapse board columns, add and rename lists from the board (M-24)` |
| Fork of record | this repository, target branch `multica-moct` |

Both bases are recorded so a reader can tell two different questions apart: what
the fork started from (`v0.6.5`), and where this plugin's rendering was last
known to be correct (`cbd75011`). A fork update that moves the second number is a
plugin change, and the island's tests are expected to be re-run with it.

## What the plugin may consume

One module — [`src/fork.ts`](src/fork.ts) — and one stylesheet —
[`src/client/island.css`](src/client/island.css). Nothing else under
`packages/dsh-plugin/src` may name an `@multica/*` module, and no source file may
reach outside the package with a relative path.

`scripts/check-fork-entry.mjs` enforces both, and it also requires each specifier
used by those two files to be a **published export** of its package (present in
that package's `package.json` `exports`). `pnpm lint` runs it, so a deep import
fails the build rather than landing in a review.

Two consequences are deliberate:

- The plugin depends on the fork's public surface, not on whatever a file happens
  to export. A fork refactor cannot silently change what the island imports.
- The island's surface is enumerable. `src/fork.ts` is the complete list of fork
  behaviour this panel depends on, and it is meant to stay short.

## Divergence policy

The fork is allowed to diverge from upstream — that is why it exists. This plugin
is not. Rules, in order:

1. **Hide, do not delete.** Surfaces the panel must not show (GitHub/Slack
   integrations, billing, autopilots, squads) are hidden in the island, not
   removed from the fork. A hidden surface stays reachable from the web app.
2. **Prefer fork changes over plugin workarounds.** If the island needs a
   capability, add it to the fork behind a normal public export and import it in
   `src/fork.ts`. Do not fork behaviour by CSS overrides or DOM surgery — those
   are invisible to the gate and break on the next fork update.
3. **Transport stays on the host.** The island talks to `/multica/*` on the dsh
   origin and nothing else. No Multica URL or token ever reaches the browser, and
   no request is CORS-shaped. A future fork change that requires the browser to
   know a Multica origin is an architectural change, not a plugin tweak.
4. **React 19 is not negotiable.** The island ships its own React 19 in a shadow
   root because dsh's shell is React 18. The fork must never be downgraded to
   match the shell; the boundary is one hook-free component per side.
5. **Record the base.** When the fork's rendering surface moves, update the fork
   base above in the same change that adapts the island.

## Updating the fork base

1. Move the fork base to the new commit and read the diff for anything in
   `packages/core`, `packages/ui` or `packages/views` that the exports in
   `src/fork.ts` depend on.
2. `pnpm --filter @multica/dsh-plugin test` — the island tests render the board
   and an issue against a stubbed backend, so a changed provider or route
   contract shows up there rather than in a browser.
3. `pnpm lint` in the package for the gate, then the workspace `pnpm -w build`.
