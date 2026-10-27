# @multica/dsh-plugin

A DeepSeek Harness client plugin that renders Multica's issues, agents, skills,
inbox and chat in the shell's `main` column, with one rail row per view in the
sidebar.

Two halves, one package:

| Face | Entry | Runs in | Owns |
| --- | --- | --- | --- |
| host | `src/index.ts` | the dsh process | `/multica` HTTP + WebSocket transport, and the `/moct` memory seam |
| client | `src/client/index.ts` | the dsh browser shell | six rail rows, their `main` seats, the React 19 island |

The host half exists so the Multica credential never leaves the dsh process: the
island calls `/multica/api/*` on its own origin, the host half forwards to the
Multica server named in the plugin config, and the browser sees neither the
server URL nor the token. Same origin means no CORS, and a WebSocket goes through
`/multica/ws` on the same seam.

`/moct` is the same idea for the memory plane, but closed: moct-be has no auth
and trusts whoever names a workspace, so the host serves a fixed allow-list of
memory routes and injects `workspaceId`, `expertId` and `actor` itself. A route
the panel does not need is a 404 that never reaches the backend; see
[`docs/moct-seam.md`](../../docs/moct-seam.md) and
the config comment in [`cordis.patch.yml`](cordis.patch.yml).

## Configure

The plugin is a normal dsh host row, declared in
[`cordis.patch.yml`](cordis.patch.yml):

```yaml
- id: multica-dsh-plugin
  name: '@multica/dsh-plugin'
  config:
    serverUrl: http://127.0.0.1:8080
    token: ''
    workspaceSlug: ''
```

`token` may be left empty and supplied as `MULTICA_DSH_TOKEN` in the dsh process
instead, which is the option to prefer on a shared machine. `workspaceSlug` empty
selects the first workspace the token can read.

A row that is missing either value still mounts: `/multica/config` reports the
reason, and the panel says what is missing instead of the rail row disappearing.

## The sidebar menu

Six rail rows, one per Multica view. Each row is a `sidebar.panellist` entry
with its own list id, and each id addresses a `main` cell of the same key — that
pairing is how dsh turns "select this row" into "render this panel", so the menu
is six rows rather than a dropdown the plugin would have to position inside
someone else's column.

| Row | Opens |
| --- | --- |
| Multica Issues | the board, and one issue at `issues/<id>` |
| Multica Agents | the agent registry, and one agent at `agents/<id>` |
| Multica Skills | skill management, and one skill at `skills/<id>` |
| Multica Inbox | the question/approval inbox an agent's `ask` and plan reviews land in |
| Multica Chat | the chat view, thread selection in `?session=` |
| Multica Settings | the fork's settings page, with the MOCT group added and the web-only groups hidden |

Each entry opens the fork's own page for that surface, under the same
`CoreProvider`; nothing is re-implemented and nothing is outlined in an iframe.
The island's router maps those six families of workspace path (plus the detail
depth above) and refuses everything else, so a shared component cannot open a
surface the panel does not own.

Settings is the fork's page with two additive props: `hiddenTabs` drops the
pages this shell cannot render (billing, GitHub hosting, messaging channels,
plugins, connected apps, the shortcut reference — the island swallows key
events before the fork sees them), and `extraGroups` appends the one group that
only exists in dsh. Hidden pages leave the navigation, the compact selector and
the search index together, so no result lands on a page that is not there.

The row chooses where an island *opens*; from there the fork's pages navigate as
they do in the web app — an inbox row that no longer resolves falls back to its
issue, for instance — and the row keeps marking the entry the reader came
through. dsh gives a plugin no way to move the shell's rail selection, so the
island follows the reader instead of fighting the rail.

Not mapped: the creation flows (`agents/new`, the AI builder). They are real
routes in the fork, but they are authoring surfaces rather than views of what
already exists, so a "New agent" click logs one line and stays put rather than
opening a page this cut does not cover.

## How it renders

`dsh.client` declares the client bundle, and dsh fetches it as one classic script.
That bundle contains its own React 19 and React DOM — the shell's React 18 never
renders island components, and the island never calls a shell hook. The two trees
meet at exactly one element per component: the shell creates it with its own
`createElement`, and the island takes over everything inside it.

Inside is a shadow root. The fork's stylesheet is compiled to one string, `:root`
is rewritten to `:host`, and the result is attached to the shadow root — so
Tailwind's preflight, the fork's design tokens and its utility classes cannot
reach dsh's document, and dsh's CSS cannot reach in. Overlays (dialogs, menus,
tooltips, popovers) render into a portal seat inside the same shadow root, which
is what keeps them styled and focus-trapped inside the island.

There is no iframe and no loopback page: the board, the filters, the issue page
and the other four views are the fork's own components, mounted under a fork
`CoreProvider` whose `ApiClient` points at `/multica`.

What the plugin may import from the fork is deliberately narrow — see
[`FORK.md`](FORK.md). One module, public exports only, enforced by `pnpm lint`.

## Hide, do not delete

Surfaces that do not belong in the harness panel — GitHub/Slack integrations,
billing, autopilots, squads — are hidden in the island. They are not removed from
the fork and remain reachable from the Multica web app.

## Develop

```sh
pnpm --filter @multica/dsh-plugin build     # tsc for the host half, then the client bundle
pnpm --filter @multica/dsh-plugin test      # proxy, gate, seat, island and storage tests
pnpm --filter @multica/dsh-plugin lint      # eslint + the fork-entry gate
pnpm --filter @multica/dsh-plugin typecheck
```

`build` writes `lib/index.js` (host) and `lib/client.js` (the wrapped client
bundle). The client bundle is generated, not hand-edited: `scripts/wrap-client.mjs`
wraps the Vite output in the module-loader preamble dsh expects.

## The host half ships as emitted

dsh imports `lib/*.js` as bare Node ESM, so the compiled output has to be
loadable without a bundler or a post-processing step:

- relative imports are written with their extension (`./proxy.js`), which tsc
  resolves to the `.ts` source and emits verbatim;
- `ws` is loaded through [`src/ws.ts`](src/ws.ts) — `createRequire` at module
  scope — because it is CommonJS-only and the host's conditions can otherwise
  select its browser build, which has no `WebSocketServer`.

Both constraints fail at dsh boot, not at build, which is why the lane's bundle
script loads the emitted entry as ESM before it ships it.
