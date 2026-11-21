# Task G4 — reversible migration (SQLite)

Work in the current directory. Write exactly TWO files, `up.sql` and `down.sql`. Each file holds one or more SQLite statements separated by `;` — no other syntax, no procedural code.

Context: the workspace tiers table `model_tier` stores a `quality` score (0–100) that the Tiers UI shows. The column is being added now, and the migration must roll back cleanly without losing rows.

Existing schema (already applied when your scripts run):

```sql
CREATE TABLE model_tier (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    name TEXT NOT NULL,
    idx INTEGER NOT NULL
);
```

## Requirements

- `up.sql`: add `quality` to `model_tier` as `INTEGER NOT NULL DEFAULT 0`. Existing rows keep their data and read back `0`; a later insert that omits `quality` also gets `0`.
- `down.sql`: remove `quality` and leave every row and every other column exactly as it was.
- No data copying, no dropping/recreating `model_tier` (rows must survive), no `PRAGMA` tricks.

Write both files, then stop.
