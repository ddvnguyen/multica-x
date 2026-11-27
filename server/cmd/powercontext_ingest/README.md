# PowerContext corpus ingest

Mirrors one workspace's current issue decisions and human-authored issue
comments into its PowerContext scope, so the memory comparison has a corpus on
both planes. See `docs/powercontext-seam.md` for the rules and what is
deliberately left out.

From `server/`:

```bash
export POWERCONTEXT_BASE_URL=http://127.0.0.1:8000   # POWERCONTEXT_TOKEN if required
export DATABASE_URL=...                              # defaults to the local dev database

go run ./cmd/powercontext_ingest --workspace <workspace uuid> --dry-run
go run ./cmd/powercontext_ingest --workspace <workspace uuid>
```

`--dry-run` prints one line per entry — marker, scope, remembered kind — and
writes nothing; `POWERCONTEXT_BASE_URL` is not needed for it. `--since`
(RFC3339) bounds a pass to rows created after a time, `--limit` bounds rows per
source (default 500).

Safe to repeat: every entry carries a marker naming its source comment or
decision, and an entry whose marker is already in the scope is skipped. An
entry whose dedupe search fails is **not** written — a missing entry is retried
by the next pass, a duplicate would be recalled forever. The command exits
non-zero when any entry failed and names them; that is the signal for a
scheduled run.

Nothing runs this on a timer. Run it manually after a deployment, or wire it
into whatever the host already uses for periodic jobs.
