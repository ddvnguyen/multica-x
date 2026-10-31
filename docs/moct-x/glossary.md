# Glossary

| Term | Meaning |
|---|---|
| **Work plane** | Everything `multica-be` owns in Postgres: issues, threads, board, skills, agent registry, concurrency/queue. One writer: `multica-be`. |
| **Memory plane** | Everything `moct-be` owns in its Turso file: typed memory records (decisions, conventions, how-to-work, notes), ledger, usage, expertise. One writer: `moct-be`. |
| **Seam** | The single typed HTTP contract between the two planes, implemented entirely in this fork (`server/internal/moct/` + `server/internal/moctmcp/` + two daemon hooks). Contract: [`docs/moct-seam.md`](../moct-seam.md); map: [seam.md](seam.md). |
| **Lane** | An isolated, loopback-only deployment of Multica + MOCT + dsh for proving changes. Lane **z** is this repo's proving lane (`deploy/dsh-z/`); lane **x** is production, deployed outside this repo. |
| **Island** | The forked Multica UI mounted inside the dsh shell: a self-contained React 19 tree in a shadow root, fed only through `packages/dsh-plugin/src/fork.ts`. See [dsh-plugin.md](dsh-plugin.md). |
| **Ref** | A stable cross-reference string (`multica:ws:…`, `multica:agent:…`, `multica:issue:…`, `multica:project:…`) that keys MOCT memory to Multica work items. `server/internal/moct/refs.go`. |
| **Interaction item** | *(design)* A planned first-class Multica entity for a parked ask/plan decision. Today a parked question is a plain transcript line; see [interactions.md](interactions.md#design-not-built). |
| **Park (a run)** | A dsh run stopped mid-flight waiting for a human decision (`question` frame emitted, run resumes on the `answer` command). See [interactions.md](interactions.md). |
