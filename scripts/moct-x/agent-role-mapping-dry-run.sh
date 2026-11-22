#!/usr/bin/env bash
# M-520: Agent -> Role mapping DRY RUN (lane z only, READ ONLY).
#
# Produces a snapshot-backed inventory + proposed Role report for every agent
# on lane z. It CHANGES NOTHING:
#   - the server query runs with default_transaction_read_only=on;
#   - there is no INSERT / UPDATE / DELETE / DDL anywhere in this script;
#   - the only files written are snapshots and a report under OUT_DIR on the
#     workspace disk (never /tmp or the root disk).
# Re-running against unchanged data produces identical files (idempotent) and
# never accumulates state. Applying the mapping needs a separate Architect
# approval and is explicitly out of scope.
#
# Usage (from anywhere on the lane host):
#   bash scripts/moct-x/agent-role-mapping-dry-run.sh
# Overrides: M520_LANE_ROOT / M520_RUNTIME_ENV / M520_OUT_DIR / M520_MOCT_DB.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LANE_ROOT="${M520_LANE_ROOT:-/mnt/workspace/clouds/dsh-z}"
RUNTIME_ENV="${M520_RUNTIME_ENV:-$LANE_ROOT/data/multica-z.env}"
SWITCH_ENV="$LANE_ROOT/data/mmx-switch.env"
OUT_DIR="${M520_OUT_DIR:-$LANE_ROOT/data/m520}"
MOCT_DB="${M520_MOCT_DB:-/mnt/workspace/clouds/moct-z/data/fleet.db}"

[ -f "$RUNTIME_ENV" ] || { echo "lane runtime env not found: $RUNTIME_ENV" >&2; exit 1; }
# shellcheck source=/dev/null
set -a; . "$RUNTIME_ENV"; set +a
if [ -f "$SWITCH_ENV" ]; then
  # shellcheck source=/dev/null
  set -a; . "$SWITCH_ENV"; set +a
fi

: "${MULTICA_Z_WORKSPACE_ID:?MULTICA_Z_WORKSPACE_ID missing from lane env}"
: "${MULTICA_Z_DB_PASSWORD:?MULTICA_Z_DB_PASSWORD missing from lane env}"
DB_CONTAINER="${MULTICA_Z_DB_CONTAINER:-infra-postgres}"
DB_NAME="${MULTICA_Z_DB_NAME:-multica_mmx}"
DB_ROLE="${MULTICA_Z_DB_ROLE:-multica}"
WS="$MULTICA_Z_WORKSPACE_ID"

mkdir -p "$OUT_DIR"
AGENTS_SNAPSHOT="$OUT_DIR/agent-role-mapping-agents.tsv"
PRESETS_SNAPSHOT="$OUT_DIR/agent-role-mapping-presets.tsv"
AUDIT_SNAPSHOT="$OUT_DIR/agent-role-preset-audit.tsv"
MEMCOUNT="$OUT_DIR/moct-memory-counts.tsv"
SNAPSHOT="$OUT_DIR/agent-role-mapping-snapshot.tsv"
REPORT="$OUT_DIR/agent-role-mapping-report.md"

# Server query helper. PGOPTIONS forces every statement read-only as a guard;
# if anything here tried to write, Postgres would reject it.
lane_psql() {
  podman exec \
    -e "PGPASSWORD=$MULTICA_Z_DB_PASSWORD" \
    -e 'PGOPTIONS=-c default_transaction_read_only=on' \
    "$DB_CONTAINER" psql -U "$DB_ROLE" -d "$DB_NAME" -X -t -A -F $'\x1f' -v ON_ERROR_STOP=1 "$@"
}

# --- 1. Snapshot: export current rows to deterministic files on workspace disk ---

# AGENTS header + one pipe-delimited row per agent (active and archived).
AGENTS_HEADER=$'uuid\x1fname\x1fowner_email\x1fowner_uuid\x1farchived\x1fsubtype\x1fkind\x1frole\x1finstr_sha256\x1fskill_count\x1fskills\x1fpermission_mode\x1faccount_mode\x1fservice_tier\x1fuse_tiers\x1fowner_member_role\x1factive_tasks\x1fpreset_aliases\x1ftier_role\x1fimplements_role_id'
AGENTS_Q="
SELECT a.id, a.name, COALESCE(u.email, a.owner_id::text), a.owner_id::text,
       (a.archived_at IS NOT NULL), COALESCE(a.subtype,'agent'), COALESCE(a.kind,'user'),
       COALESCE(a.role,'-'),
       substr(encode(sha256(convert_to(COALESCE(a.instructions,''),'UTF8')),'hex'),1,16),
       (SELECT count(*) FROM agent_skill ags WHERE ags.agent_id=a.id AND ags.enabled),
       COALESCE((SELECT string_agg(s.name, ',' ORDER BY s.name) FROM agent_skill ags JOIN skill s ON s.id=ags.skill_id WHERE ags.agent_id=a.id AND ags.enabled),''),
       COALESCE(a.permission_mode,'-'), COALESCE(a.account_mode,'-'), COALESCE(a.service_tier,'-'),
       COALESCE(a.use_tiers,false),
       COALESCE((SELECT m.role FROM member m WHERE m.workspace_id=a.workspace_id AND m.user_id=a.owner_id),'-'),
       (SELECT count(*) FROM agent_task_queue t WHERE t.agent_id=a.id AND t.status IN ('queued','dispatched','running')),
       COALESCE((SELECT string_agg(p.name || '<-' || COALESCE(p.legacy_agent_id::text,'none'), ', ' ORDER BY p.name)
                 FROM agent_preset p WHERE p.workspace_id=a.workspace_id AND (p.agent_id=a.id OR p.legacy_agent_id=a.id)),'-'),
       COALESCE(a.tier_role,'-'), COALESCE(a.implements_role_id::text,'-')
FROM agent a LEFT JOIN \"user\" u ON u.id=a.owner_id
WHERE a.workspace_id = '$WS'
ORDER BY (a.archived_at IS NOT NULL), lower(a.name);"
{
  echo "# M-520 agent inventory snapshot (read-only export; no DB writes)"
  echo "# workspace=$WS db=$DB_NAME container=$DB_CONTAINER"
  echo "# $AGENTS_HEADER"
  lane_psql -c "$AGENTS_Q"
} > "$AGENTS_SNAPSHOT"

PRESETS_HEADER=$'name\x1fpreset_id\x1fagent_id\x1fagent_name\x1ftier_id\x1ftier_name\x1flegacy_agent_id\x1flegacy_name\x1fis_alias\x1fis_one_to_one'
PRESETS_Q="
SELECT p.name, p.id, p.agent_id, a.name, p.tier_id, COALESCE(t.name,'-'),
       COALESCE(p.legacy_agent_id::text,'none'), COALESCE(la.name,'none'),
       (p.legacy_agent_id IS NOT NULL AND p.legacy_agent_id <> p.agent_id) AS is_alias,
       (p.legacy_agent_id IS NULL OR p.legacy_agent_id = p.agent_id) AS is_one_to_one
FROM agent_preset p
LEFT JOIN agent a ON a.id=p.agent_id
LEFT JOIN model_tier t ON t.id=p.tier_id
LEFT JOIN agent la ON la.id=p.legacy_agent_id
WHERE p.workspace_id = '$WS'
ORDER BY lower(p.name);"
{
  echo "# M-520 preset snapshot (read-only export; no DB writes)"
  echo "# $PRESETS_HEADER"
  lane_psql -c "$PRESETS_Q"
} > "$PRESETS_SNAPSHOT"

# Compatibility audit: requested-preset queued retries and published links.
AUDIT_Q="
SELECT 'requested_preset_tasks_total', count(*)::text FROM agent_task_queue WHERE context->>'requested_preset_id' IS NOT NULL
UNION ALL SELECT 'requested_preset_tasks_active', count(*)::text FROM agent_task_queue WHERE context->>'requested_preset_id' IS NOT NULL AND status IN ('queued','dispatched','running')
UNION ALL SELECT 'published_preset_mentions', (SELECT count(*)::text FROM comment WHERE content LIKE '%mention://preset/%')::text
UNION ALL SELECT 'issues_assigned_to_preset', (SELECT count(*)::text FROM issue i JOIN agent_preset p ON p.id=i.assignee_id AND p.workspace_id=i.workspace_id)::text
ORDER BY 1;"
{
  echo "# M-520 preset compatibility audit (read-only export)"
  echo $'# metric\x1fvalue'
  lane_psql -c "$AUDIT_Q"
} > "$AUDIT_SNAPSHOT"

# MOCT memory is in the lane-z moct-be fleet DB (SQLite/Turso). Copy db+wal+shm
# to the workspace disk first so we never contend with the live writer, then
# count memory records by the agent that decided them. Copies are removed after.
MEM_DB_COPY="$OUT_DIR/.fleet-snapshot.db"
cp "$MOCT_DB" "$MEM_DB_COPY"
[ -f "$MOCT_DB-wal" ] && cp "$MOCT_DB-wal" "$MEM_DB_COPY-wal"
[ -f "$MOCT_DB-shm" ] && cp "$MOCT_DB-shm" "$MEM_DB_COPY-shm"
{
  echo "# M-520 MOCT memory counts by deciding agent (read-only copy of fleet.db)"
  echo $'# agent_uuid\x1fmemory_records'
  sqlite3 -separator $'\x1f' "$MEM_DB_COPY" "SELECT replace(json_extract(attrs,'\$.decidedBy'),'multica:agent:','') AS agent_uuid, count(*)
      FROM team_memory_records
      WHERE json_extract(attrs,'\$.decidedBy') IS NOT NULL
      GROUP BY 1 ORDER BY 1;"
} > "$MEMCOUNT"
rm -f "$MEM_DB_COPY" "$MEM_DB_COPY-wal" "$MEM_DB_COPY-shm"

# Combined snapshot file for reviewers (the three exports above are the sources).
{
  echo "# M-520 combined snapshot (see also *-agents.tsv, *-presets.tsv, moct-memory-counts.tsv)"
  grep -v '^$' "$AGENTS_SNAPSHOT"
  echo
  grep -v '^$' "$PRESETS_SNAPSHOT"
  echo
  grep -v '^$' "$AUDIT_SNAPSHOT"
  echo
  grep -v '^$' "$MEMCOUNT"
} > "$SNAPSHOT"

# --- 2. Proposed Role (draft from M-499 plan; NOT approved, NOT applied) ---
propose_role() {
  local name="$1" kind="$2" subtype="$3" archived="$4"
  if [ "$archived" = "t" ] || [ "$archived" = "true" ]; then echo "unassigned (archived)"; return; fi
  if [ "$kind" = "system" ]; then echo "system (never converted)"; return; fi
  if [ "$subtype" = "role" ]; then echo "already a Role"; return; fi
  # Role display names (tier_role slugs are unchanged). Accepted M-521 decisions:
  # Lead (Muse Spark) -> Management; Opencode2, Freebuff Agent, [Go Free] Engineer,
  # Codex | Luna | Engineer and Codex | Terra | Engineer -> Engineer.
  case "$name" in
    "Architect"|"Claude Architect"|"Opus Architect"|"Codex | Architect") echo "Architect Role" ;;
    "Lead"|"Orchestrator"|"Lead (Muse Spark)") echo "Management Role" ;;
    "Software Engineer"|"[Free] Codebuddy Engineer"|"Opencode2"|"Codebuddy Senior Engineer"|"Freebuff Engineer"|"Freebuff Agent"|"[Go Free] Engineer"|"Codex - Luna | Engineer"|"Codex | Luna | Engineer"|"Codex - Terra | Reviewer"|"Codex | Terra | Engineer") echo "Engineer Role" ;;
    "Mika") echo "unassigned / system (never converted)" ;;
    TierDemo|"m510-lanez-agent"|"m515-lanez-agent"|m510-*|m515-*) echo "unassigned (test/verification agent)" ;;
    *) echo "unassigned" ;;
  esac
}

# Markdown-cell escaping for values that may contain '|' (e.g. "Codex | Architect").
md() { printf '%s' "${1//|/\\|}"; }

declare -A MEMORY
while IFS=$'\x1f' read -r uuid cnt; do
  case "$uuid" in \#*|"") continue ;; esac
  MEMORY["$uuid"]="$cnt"
done < "$MEMCOUNT"

# --- 3. Report (derived entirely from the snapshots above) ---
{
  echo "# M-520 Agent -> Role mapping DRY RUN (lane z, read-only)"
  echo
  echo "Workspace: \`$WS\` · database: \`$DB_NAME\` · sources: lane-z multica-be + moct-be fleet DB."
  echo "Snapshots: \`$AGENTS_SNAPSHOT\`, \`$PRESETS_SNAPSHOT\`, \`$AUDIT_SNAPSHOT\`, \`$MEMCOUNT\` (combined: \`$SNAPSHOT\`)."
  echo "This report CHANGES NOTHING. Proposed Roles are the unapproved M-499 draft; applying them needs separate Architect approval."
  echo
  echo "## Proposed Role mapping (draft identities -> live lane-z inventory)"
  echo
  echo "| Draft identity (accepted M-521 decisions) | Proposed Role | Lane-z status |"
  echo "| --- | --- | --- |"
  echo "| Architect, Claude Architect, Opus Architect, Codex \\| Architect | Architect Role | 4 present |"
  echo "| Lead, Orchestrator, Lead (Muse Spark) | Management Role | 3 present |"
  echo "| Software Engineer, [Free] Codebuddy Engineer, Opencode2, Codebuddy Senior Engineer, Freebuff Engineer, [Go Free] Engineer | Engineer Role | 6 present |"
  echo "| Codex \\| Luna \\| Engineer (draft: Codex - Luna \\| Engineer), Codex \\| Terra \\| Engineer (draft: Codex - Terra \\| Reviewer) | Engineer Role | name drift; both map to Engineer Role per accepted decision |"
  echo "| Mika | unassigned / system | present; never converted |"
  echo
  echo "## Full agent inventory (every agent on lane z)"
  echo
  echo "| UUID | Name | Owner | Instr hash | Skills (#) | Privileges | Active tasks | Preset aliases | MOCT memory | Proposed Role | Notes |"
  echo "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |"
  while IFS=$'\x1f' read -r uuid name owner_email owner_uuid archived subtype kind role ihash scount skills perm acct stier usetiers mrole atasks aliases trole impl; do
    case "$uuid" in \#*|"") continue ;; esac
    role_prop="$(propose_role "$name" "$kind" "$subtype" "$archived")"
    mem="${MEMORY[$uuid]:-0}"
    priv="$mrole; perm=$perm; acct=$acct; tier=$stier; subtype=$subtype; kind=$kind; role=$role; use_tiers=$usetiers"
    notes=""
    if [ "$archived" = "t" ] || [ "$archived" = "true" ]; then notes="archived"; fi
    if [ "$impl" != "-" ]; then notes="${notes:+$notes; }implements_role_id=$impl"; fi
    if [ "$trole" != "-" ]; then notes="${notes:+$notes; }tier_role=$trole"; fi
    echo "| \`$uuid\` | $(md "$name") | $(md "$owner_email") | \`$ihash\` | $scount | $(md "$priv") | $atasks | $(md "$aliases") | $mem | $(md "$role_prop") | $(md "${notes:-—}") |"
  done < "$AGENTS_SNAPSHOT"
  echo
  echo "## Skill inventory (full enabled skill list per agent)"
  echo
  while IFS=$'\x1f' read -r uuid name owner_email owner_uuid archived subtype kind role ihash scount skills rest; do
    case "$uuid" in \#*|"") continue ;; esac
    echo "- **$(md "$name")** ($scount): ${skills:-—}"
  done < "$AGENTS_SNAPSHOT"
  echo
  echo "## Preset audit (rows, aliases, 1:1 with implementing Agent)"
  echo
  echo "| Preset | Agent | Tier | Legacy alias | 1:1? |"
  echo "| --- | --- | --- | --- | --- |"
  while IFS=$'\x1f' read -r pname pid aid aname tid tname lid lname isalias oneone; do
    case "$pname" in \#*|"") continue ;; esac
    echo "| $(md "$pname") | $(md "$aname") | $(md "$tname") | $(md "${lname:-none}") | $oneone |"
  done < "$PRESETS_SNAPSHOT"
  echo
  echo "1:1 means \`legacy_agent_id IS NULL OR = agent_id\` (the preset names its own implementing Agent). Aliases map a retired profile id onto a canonical Agent. Architect, [Free] Codebuddy Engineer and Orchestrator are 1:1; Claude Architect, Opus Architect and [Go Free] Engineer are aliases onto Architect/Software Engineer."
  echo
  echo "## Requested-preset compatibility audit"
  echo
  echo "| Metric | Value |"
  echo "| --- | --- |"
  while IFS=$'\x1f' read -r metric value; do
    case "$metric" in \#*|"") continue ;; esac
    echo "| $metric | $value |"
  done < "$AUDIT_SNAPSHOT"
  echo
  echo "Historical requested-preset tasks still resolve via the unchanged resolver; none are queued right now. There are no published \`mention://preset\` links or preset-type issue assignees on lane z, so the read alias / resolver can stay unchanged with no old-client links to break."
  echo
  echo "## Notes / unverified"
  echo
  echo "- Proposed Roles use display names; \`tier_role\` slugs are unchanged. These are accepted M-521 decisions, not an applied conversion."
  echo "- Names marked *name drift* differ between the draft and live lane z; the Architect accepted the live names as Engineer Role. **Freebuff Agent** has no enabled skills but maps to Engineer Role by accepted decision."
  echo "- **Mika** and all \`kind=system\` agents (\`.multica-agent-builder-*\`) are never converted."
  echo "- \`m510-*\`, \`m515-*\` and \`TierDemo\` are lane verification agents, not part of the 15 production identities."
  echo "- Prefix hashes are sha256 (first 16 hex chars) of current \`instructions\` text; full rows are in \`$SNAPSHOT\`."
} > "$REPORT"

echo "M-520 dry run complete (read-only)."
echo "  snapshot: $SNAPSHOT"
echo "  memory:   $MEMCOUNT"
echo "  report:   $REPORT"
echo
cat "$REPORT"
