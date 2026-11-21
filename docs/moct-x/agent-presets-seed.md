# M-471 lane-z preset seed

`scripts/seed-agent-presets.sql` is an operator-run, workspace-scoped seed. It is intentionally not a migration: applying it as a normal migration would change every environment, while M-471 authorizes lane z only.

## Reviewed mapping

The mapping is the lane-z revision approved by Claude Architect after the initial snapshot showed that M-423 tiers were curated independently from legacy profile runtime/model/thinking values. The script checks each listed profile ID and name against the target workspace, checks the canonical position agent and role, and resolves the explicitly named tier in that workspace with the required role. A missing/ambiguous profile or tier, or a tier without that role, aborts before writes.

| Legacy profile | Canonical position | Role | Tier | Expected fidelity |
| --- | --- | --- | --- | --- |
| Architect | Architect | architect | `arch-glm-flash` | exact |
| Claude Architect | Architect | architect | `arch-sonnet-5-5` | exact |
| Opus Architect | Architect | architect | `arch-opus-5-5` | exact |
| Orchestrator | Orchestrator | management | `orch-beam` | model and thinking match; runtime differs |
| [Go Free] Engineer | Software Engineer | engineer | `eng-beam` | model and runtime match; thinking differs |
| [Free] Codebuddy Engineer | [Free] Codebuddy Engineer | engineer | `eng-cb-deepseek` | model matches; runtime and thinking differ |

The snapshot reports each tier's ordered live member runtime/model/thinking values and enabled state, plus computed fidelity across enabled members: `exact` when any enabled member matches all three legacy values, `model-only` when no enabled member is exact but at least one has the same model, and `differs` when no enabled member has the same model. A tier without an enabled member fails preflight.

The unmapped profiles `Codex | Architect`, `Lead`, `Lead (Muse Spark)`, `Software Engineer`, `Codebuddy Senior Engineer`, and `Orchestrator (Backup)` are left unchanged. The five canonical position agents still receive the full set of tiers for their role, including Lead's `management` role; `management` is M-481's persisted role vocabulary.

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
