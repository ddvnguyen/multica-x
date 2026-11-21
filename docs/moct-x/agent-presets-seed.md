# M-471 lane-z preset seed

`scripts/seed-agent-presets.sql` is an operator-run, workspace-scoped seed. It is intentionally not a migration: applying it as a normal migration would change every environment, while M-471 authorizes lane z only.

## Reviewed mapping

The mapping comes from the M-468 inventory proposal accepted by Claude Architect. The script checks each exact profile ID and name against the target workspace, checks the canonical position agent and role, and resolves exactly one tier from the profile's runtime, model, thinking level, and role. Any missing or ambiguous match aborts before writes.

| Legacy profile | Canonical position | Role |
| --- | --- | --- |
| Architect; Claude Architect; Opus Architect; Codex \| Architect | Architect | architect |
| Lead; Lead (Muse Spark) | Lead | management |
| Orchestrator; Orchestrator (Backup) | Orchestrator | management |
| Software Engineer; [Go Free] Engineer | Software Engineer | engineer |
| [Free] Codebuddy Engineer; Codebuddy Senior Engineer | [Free] Codebuddy Engineer | engineer |

The other 14 profiles from the inventory are not listed and are left unchanged. `management` reflects M-481's persisted role vocabulary for Lead and Orchestrator.

## Snapshot, apply, rollback

Run against the lane-z database only. The first run is read-only and returns two result sets: each preset/alias and selected tier, then each canonical agent's role-tier list with existing membership state. Save the complete output on the workspace disk and review every row against the lane before applying:

```sh
psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 \
  -v workspace_id='<lane-z-workspace-uuid>' -v apply=false \
  -f scripts/seed-agent-presets.sql > /mnt/workspace/agent-preset-seed-snapshot.tsv
```

After review, re-run against the same workspace with `-v apply=true -v snapshot_reviewed=true`. The explicit marker prevents accidental application on the snapshot-only pass. The script repeats all checks inside a transaction, adds missing presets and agent-tier memberships, enables tier selection on the five canonical agents, and turns on `agent_presets_enabled`. It never deletes or updates legacy profile, assignment, task, or mention rows. Repeated apply runs are idempotent.

To roll back the rollout, set `agent_presets_enabled` to `false` for that workspace. Seeded rows remain for inspection and can be cleaned up in a separately reviewed operation.

The script cannot confirm a real tier-routed run or a visible fallback; those require lane-z runtime access after the reviewed snapshot is applied.
