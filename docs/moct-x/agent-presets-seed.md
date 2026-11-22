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

After review, re-run against the same workspace with `-v apply=true -v snapshot_reviewed=true`. The explicit marker prevents accidental application on the snapshot-only pass. The script repeats all checks inside a transaction and never deletes or updates legacy profile, assignment, task, or mention rows.

## M-520: seeding frozen (no retirement)

The M-471 rollout already ran on lane z. Per the M-499 approved plan and the Owner's answer (presets are kept, not retired), **preset seeding is frozen**: from M-520 onward the script's apply branch is a documented no-op and no code path adds new `agent_preset` rows. The `-v apply=true -v snapshot_reviewed=true` invocation still runs every preflight check and prints the same snapshot result sets, but writes nothing — no preset rows, no `agent_tier` memberships, no `use_tiers` changes and no workspace-settings change. The snapshot is now the script's complete output.

Nothing about compatibility is retired: existing preset rows, the requested-preset resolver, the `legacy_agent_id` read aliases and the CreateAgentPreset API all keep working unchanged. Rolling back the rollout is still `agent_presets_enabled = false` for that workspace. Any future preset ↔ Role conversion (two-way, per the Owner's answer) requires a separate, Architect-approved and reviewed script; applying the agent→Role mapping is explicitly out of scope for M-520.

The script cannot confirm a real tier-routed run or a visible fallback; those require lane-z runtime access. The M-520 Go tests pin requested-preset resolution, requested-vs-actual tier stamping, fallback and retry re-authorization.
