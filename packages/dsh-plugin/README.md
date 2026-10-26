# @multica/dsh-plugin

A DeepSeek Harness client plugin that renders the Multica board and issue page in
the shell's `main` column, with a rail row in the sidebar.

Two halves, one package:

| Face | Entry | Runs in | Owns |
| --- | --- | --- | --- |
| host | `src/index.ts` | the dsh process | `/multica` HTTP + WebSocket transport |
| client | `src/client/index.ts` | the dsh browser shell | the rail row, the `main` seat, the React 19 island |

The host half exists so the Multica credential never leaves the dsh process: the
island calls `/multica/api/*` on its own origin, the host half forwards to the
Multica server named in the plugin config, and the browser sees neither the
server URL nor the token. Same origin means no CORS, and a WebSocket goes through
`/multica/ws` on the same seam.

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

There is no iframe and no loopback page: the board, the filters and the issue page
are the fork's own components, mounted under a fork `CoreProvider` whose
`ApiClient` points at `/multica`.

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
