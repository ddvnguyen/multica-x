# PowerContext corpus ingest

Mirrors one workspace's current issue decisions, human-authored issue comments
and active MOCT records into its PowerContext scopes, so the memory comparison
has a corpus on both planes. See `docs/powercontext-seam.md` for the rules and
what is deliberately left out.

From `server/`:

```bash
export POWERCONTEXT_BASE_URL=http://127.0.0.1:8000   # POWERCONTEXT_TOKEN if required
export MOCT_BASE_URL=http://127.0.0.1:8310           # required while MOCT records are mirrored
export DATABASE_URL=...                              # defaults to the local dev database

go run ./cmd/powercontext_ingest --workspace <workspace uuid> --dry-run
go run ./cmd/powercontext_ingest --workspace <workspace uuid>
```

**The Scope has to exist.** The command addresses the workspace's project (or
workspace) Scope by its Multica ref — `multica:project:<uuid>`, else
`multica:ws:<uuid>` — and the server resolves that ref through the Scope's
`external_references` to its `scp_…` id. PowerContext generates ids, so a ref
with no Scope carrying it fails before anything is written; create the Scope
with the ref as an external reference of kind `multica-project` (or
`multica-workspace`) first. `GET /v1/scopes?query=<ref>&query_field=external_reference_value`
is the same lookup the client does, if you want to check by hand.

MOCT records are read through `GET /team/v1/memory` (`all=1`, the default
`active` status, pages of 20). A record is written to the scope its home names:
a channel record to that project, a task record to its issue's project, a team
record to the workspace and every project of it. Expert-home records are left
out and counted — MOCT shows them to one expert and PowerContext cannot.

`--dry-run` prints one line per entry — marker, scope, remembered kind — and
writes nothing; `POWERCONTEXT_BASE_URL` is not needed for it, but
`MOCT_BASE_URL` is while MOCT records are mirrored. `--since` (RFC3339) bounds a
pass to rows created after a time (for MOCT records, updated after it);
`--limit` bounds rows per database source and defaults to `0` = unbounded.
`--moct-records=false` skips the MOCT source and its `MOCT_BASE_URL`
requirement.

Safe to repeat: every entry carries a marker naming its source comment,
decision or record, and an entry whose marker is already in the scope is
skipped. An entry whose dedupe search fails is **not** written — a missing
entry is retried by the next pass, a duplicate would be recalled forever. An
edit or a retraction in MOCT does not propagate to an entry that was already
mirrored. The command exits non-zero when any entry failed or the MOCT listing
failed, and names the entries; that is the signal for a scheduled run.

Nothing runs this on a timer. Run it manually after a deployment, or wire it
into whatever the host already uses for periodic jobs.
