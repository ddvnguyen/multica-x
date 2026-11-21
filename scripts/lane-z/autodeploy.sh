#!/usr/bin/env bash
# scripts/lane-z/autodeploy.sh — pull-based CD for lane z (M-151).
#
# The host pulls; CI never reaches the lane. dsh-z-autodeploy.timer runs this
# every 60 s as `ddv`. It asks GitHub for the newest SUCCESSFUL push run of CI
# on multica-moct, compares that head SHA with the release lane z is actually
# running (`multica-be/current/release.json`), and — only when they differ —
# downloads that run's artifacts, backs the database up, installs the release,
# migrates, switches, health-checks, and comments the evidence on the merged
# PR's card.
#
# No rebuild: every artifact comes from CI. A missing or mismatched artifact is
# a red report, never a local build.
#
# Invariants this script holds:
#   * one deploy at a time (flock); a tick that loses the lock exits 0 quietly
#   * newest SHA wins: the newest successful run is re-read after download, so
#     a superseded SHA is dropped instead of deployed
#   * a SHA that already failed is not retried every minute (data/autodeploy.failed)
#   * rollback switches binaries only — it NEVER restores the database. A
#     pg_restore over a live DB silently loses every write since the dump; the
#     dump path goes in the red report for a human to decide.
#   * the plugin bundle (and therefore dsh-z.service) is only touched when the
#     bundle content actually changed
#
# Usage:
#   autodeploy.sh                one tick (this is what the timer runs)
#   autodeploy.sh --dry-run      print what a tick would do; changes nothing
#   autodeploy.sh --sha <sha>    deploy a specific commit (still needs a green
#                                push run and its artifacts)
#   autodeploy.sh --drill-fail-health
#                                rollback drill: run a real deploy and force the
#                                post-switch health assertion to fail, so the
#                                rollback path and the red report are exercised.
#                                Posts nothing; prints the report it would send.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The timer runs the installed copy under AUTODEPLOY_BIN_DIR, where
# install-units.sh also drops dsh-z.env; a repo checkout keeps the same file two
# levels up. Sourcing the copy means the host never depends on a task checkout
# that disappears.
ENV_FILE="$SCRIPT_DIR/dsh-z.env"
[ -f "$ENV_FILE" ] || ENV_FILE="$SCRIPT_DIR/../../deploy/dsh-z/dsh-z.env"
# shellcheck source=/dev/null
. "$ENV_FILE"
# shellcheck source=./lane-lib.sh
. "$SCRIPT_DIR/lane-lib.sh"

log() { printf '[autodeploy %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

# Resolve the lane web's actual bind address/port from its systemd unit's
# ExecStart (lane z binds the podman bridge, not loopback); fall back to
# 127.0.0.1:3180 when the unit file is unavailable.
resolve_lane_web_url() {
  local unit_file="" host port
  if [ -n "${DSH_Z_WEB_UNIT:-}" ]; then
    for c in "$HOME/.config/systemd/user/$DSH_Z_WEB_UNIT" "/etc/systemd/user/$DSH_Z_WEB_UNIT" "$HOME/.local/share/systemd/user/$DSH_Z_WEB_UNIT"; do
      if [ -f "$c" ]; then unit_file="$c"; break; fi
    done
  fi
  host="$(sed -n 's/^ExecStart=.*--host \([^ ]*\).*/\1/p' "$unit_file" 2>/dev/null | head -1)"
  [ -n "$host" ] || host="127.0.0.1"
  port="$(sed -n 's/^ExecStart=.*--port \([^ ]*\).*/\1/p' "$unit_file" 2>/dev/null | head -1)"
  [ -n "$port" ] || port="3180"
  printf 'http://%s:%s' "$host" "$port"
}

dshz_guard() {
  local g="$SCRIPT_DIR/dsh-z-noauth-guard.sh"
  [ -x "$g" ] || g="$LANE_ROOT/bin/dsh-z-noauth-guard.sh"
  printf '%s' "$g"
}

dshz_smoke_record() { # writes one-line result to latest context and stdout; returns 0 on pass
  local guard line rc
  guard="$(dshz_guard)"
  DSHZ_SMOKE_LINE=""
  if [ ! -x "$guard" ]; then
    DSHZ_SMOKE_LINE="gate=fail reason=missing-guard path=$guard"
    log "dsh-z smoke: $DSHZ_SMOKE_LINE"
    return 30
  fi
  if line="$(DSH_Z_WEB_URL="$(resolve_lane_web_url)" DSH_Z_GATEWAY_URL="${DSH_Z_GATEWAY_URL:-http://127.0.0.1:3738}" "$guard" smoke 2>&1)"; then
    rc=0
  else
    rc=$?
  fi
  DSHZ_SMOKE_LINE="$(printf '%s\n' "$line" | tail -1)"
  printf '%s\n' "$DSHZ_SMOKE_LINE"
  return "$rc"
}

dshz_repair_once() { # reapply+restart+recheck once. Returns 0 on pass, else non-zero.
  local guard out rc web_unit
  guard="$(dshz_guard)"
  web_unit="${DSH_Z_WEB_UNIT:-dsh-z.service}"
  [ -x "$guard" ] || { DSHZ_REAPPLY_NOTE="failed-missing-guard"; return 20; }
  if out="$(DSH_Z_WEB_URL="$(resolve_lane_web_url)" DSH_Z_GATEWAY_URL="${DSH_Z_GATEWAY_URL:-http://127.0.0.1:3738}" "$guard" ensure 2>&1)"; then
    DSHZ_REAPPLY_NOTE="ok-no-restart"
    printf '%s\n' "$out" | tail -2 >&2 || true
    dshz_smoke_record >/dev/null && return 0
  else
    rc=$?
    printf '%s\n' "$out" | tail -4 >&2 || true
  fi
  if [ "${rc:-0}" = 20 ]; then
    DSHZ_REAPPLY_NOTE="unsupported-or-failed"
    return 20
  fi
  # rc 10 => reapplied or needs restart; rc 30 shouldn't happen here but treat as repair-needed
  DSHZ_REAPPLY_NOTE="reapplied-restart"
  log "dsh-z noauth: restarting $web_unit after reapply/repair attempt"
  systemctl --user restart "$web_unit"
  web_liveness "$(resolve_lane_web_url)" 90 || return 30
  dshz_smoke_record >/dev/null
}

dshz_verify_or_repair() { # for unchanged-tick / post-deploy checks (repair may restart web)
  local out rc guard
  guard="$(dshz_guard)"
  [ -x "$guard" ] || { DSHZ_REAPPLY_NOTE="failed-missing-guard"; return 20; }
  if out="$(DSH_Z_WEB_URL="$(resolve_lane_web_url)" DSH_Z_GATEWAY_URL="${DSH_Z_GATEWAY_URL:-http://127.0.0.1:3738}" "$guard" ensure 2>&1)"; then
    printf '%s\n' "$out" | tail -1 >&2 || true
    DSHZ_REAPPLY_NOTE="$(printf '%s\n' "$out" | tail -1 | sed -n 's/.*reapplied=\([^ ]*\).*/reapplied=\1/p')"
    [ -n "$DSHZ_REAPPLY_NOTE" ] || DSHZ_REAPPLY_NOTE="ok"
    dshz_smoke_record >/dev/null && return 0
  else
    rc=$?
    printf '%s\n' "$out" | tail -4 >&2 || true
  fi
  if [ "${rc:-0}" = 20 ]; then
    DSHZ_REAPPLY_NOTE="unsupported-or-failed"
    return 20
  fi
  # needs restart/recheck once
  DSHZ_REAPPLY_NOTE="reapplied-restart"
  log "dsh-z noauth: restarting ${DSH_Z_WEB_UNIT:-dsh-z.service} after repair"
  systemctl --user restart "${DSH_Z_WEB_UNIT:-dsh-z.service}"
  web_liveness "$(resolve_lane_web_url)" 90 || return 30
  dshz_smoke_record >/dev/null
}

# M-399 repair: the dsh 0.2.2-rc.3 web fences every route behind a browser
# session cookie, so unauthenticated probes answer 401 and wait_http (curl -f)
# can never confirm liveness. "Serving" is any HTTP status at all.
web_liveness() { # $1 = base url, $2 = timeout seconds
  local deadline=$((SECONDS + $2)) code
  while [ "$SECONDS" -lt "$deadline" ]; do
    code="$(curl -sS -o /dev/null --max-time 3 -w '%{http_code}' "$1/" 2>/dev/null || true)"
    case "$code" in [12345]*) return 0 ;; esac
    sleep 2
  done
  return 1
}

# Every failure goes through fail(): it records why and returns non-zero so the
# ERR trap runs the rollback and the red report. A bare `exit 1` skips the trap
# and leaves a half-switched lane with nobody told.
fail() { # $1 = reason
  FAIL_REASON="$1"
  log "FAILED: $1"
  return 1
}

DRY_RUN=0
DRILL_FAIL_HEALTH=0
FORCE_SHA=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --drill-fail-health) DRILL_FAIL_HEALTH=1 ;;
    --sha) shift; FORCE_SHA="${1:-}" ;;
    -h|--help) sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
  shift
done

mkdir -p "$AUTODEPLOY_LOG_DIR" "$AUTODEPLOY_BACKUP_DIR" "$LANE_ROOT/artifacts" "$LANE_ROOT/data"
LOG_FILE="$AUTODEPLOY_LOG_DIR/$(date -u +%Y%m%dT%H%M%SZ).log"
# Everything this run says also lands in its own log file, so the evidence
# comment can point at one path instead of pasting journal output.
exec > >(tee -a "$LOG_FILE") 2>&1

SHA=""
RUN_ID=""
LANE_COMMIT=""
REPORT_FAILED=0
SWITCHED=0
FAILED=0
FAIL_REASON=""
PREV_REL=""
SKIP_BACKEND_ROLLBACK=0
DSHZ_SMOKE_LINE=""
DSHZ_REAPPLY_NOTE="not-run"

on_error() { # $1 = line
  local rc=$?
  trap - ERR
  set +e
  FAILED=1
  FAIL_REASON="${FAIL_REASON:-command failed (rc=$rc) at line $1}"
  log "FAILED in step '$STEP': $FAIL_REASON"
  if [ -n "$SHA" ] && [ "$DRY_RUN" = 0 ]; then
    local failed_marker="$LANE_ROOT/data/autodeploy.failed"
    printf '%s\n' "$SHA" > "$failed_marker.tmp.$$"
    mv -f "$failed_marker.tmp.$$" "$failed_marker"
    log "recorded failed target $SHA in $failed_marker"
  fi
  if [ "$SWITCHED" = 1 ] && [ "$SKIP_BACKEND_ROLLBACK" != 1 ]; then
    log "rolling back to the previous release"
    bash "$SCRIPT_DIR/rollback.sh" || log "ROLLBACK FAILED — lane z may be on the broken release"
  elif [ "$SWITCHED" = 1 ] && [ "$SKIP_BACKEND_ROLLBACK" = 1 ]; then
    log "backend rollback skipped (web-profile/DSH-Z failure policy); lane stays on switched release"
  else
    log "nothing was switched; lane z still runs ${PREV_REL:-the previous release}"
  fi
  # report() prints instead of posting during a drill, so the red body is
  # always visible — the drill exists to prove exactly that text.
  report red
  log "log: $LOG_FILE"
  exit 1
}
trap 'on_error $LINENO' ERR

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

free_gb() { df -BG --output=avail /mnt/workspace | tail -1 | tr -dc '0-9'; }

# Every stage announces itself and clears the failure reason, so a red report
# names the step that actually broke instead of a stale one.
step() {
  STEP="$1"
  FAIL_REASON=""
  log "step: $1"
}

release_sha() { # sha of the release lane z currently runs
  local f="$LANE_ROOT/multica-be/current/release.json"
  [ -f "$f" ] || { printf 'none'; return; }
  jq -r '.sha // "none"' "$f"
}

release_version() {
  jq -r '.version // "unknown"' "$LANE_ROOT/multica-be/current/release.json" 2>/dev/null || printf 'unknown'
}

newest_green() { # newest successful push run of CI on the branch, as JSON
  gh run list --repo "$AUTODEPLOY_REPO" --workflow "$AUTODEPLOY_WORKFLOW" \
    --branch "$AUTODEPLOY_BRANCH" --event push --status success --limit 1 \
    --json headSha,databaseId,createdAt,updatedAt,url
}

run_for_sha() { # $1 = sha; run id of the successful push run for that sha
  gh run list --repo "$AUTODEPLOY_REPO" --workflow "$AUTODEPLOY_WORKFLOW" \
    --branch "$AUTODEPLOY_BRANCH" --event push --status success --limit 50 \
    --json headSha,databaseId \
    | jq -r --arg sha "$1" '[.[] | select(.headSha == $sha)][0].databaseId // empty'
}

assert_health() { # $1 = sha the lane must report
  local sha="$1" commit web
  if [ "$DRILL_FAIL_HEALTH" = 1 ]; then
    log "drill: forcing the post-switch health assertion to fail"
    return 1
  fi
  wait_http "http://127.0.0.1:$MULTICA_BE_Z_PORT/healthz" 120 '"status":"ok"' \
    || { log "multica-be z /healthz did not answer ok"; return 1; }
  # M-399 repair: dsh 0.2.2-rc.3 fences every /multica/* request behind a
  # browser-session cookie (client-connection admit()); the launch token that
  # mints one is generated in-process at startup and never persisted, so a
  # plain curl from this script always gets 401 and the old lane-web proxy
  # probes can never answer again. Assert the two halves separately instead:
  #   * the backend end-to-end: its own /healthz (above) and /health commit
  #     (the same payload the /multica/health proxy used to deliver), and
  #   * the lane web unit's liveness: any HTTP status means it is serving.
  # The browser island keeps its session cookie, so the proxied path the UI
  # uses is exercised by the per-card verification, not here.
  web="$(resolve_lane_web_url)"
  web_liveness "$web" 60 || { log "lane web did not answer HTTP within 60s"; return 1; }
  # M-387 retired the dsh /moct host proxy ("no separate /moct route"), so the
  # lane-web /moct/status is a permanent 404. Check moct-be-z directly (M-352).
  wait_http "http://127.0.0.1:${MOCT_BE_Z_PORT:-8310}/health" 60 '"ok":true' \
    || { log "moct-be z /health did not answer ok"; return 1; }
  commit="$(curl -fsS --max-time 5 "http://127.0.0.1:$MULTICA_BE_Z_PORT/health" | jq -r '.commit // empty')" \
    || { log "multica-be z /health did not answer"; return 1; }
  commit_matches "$commit" "$sha" \
    || { log "/health reports commit '$commit', expected '$sha'"; return 1; }
  # CI-built releases answer with the short sha; the report quotes what the lane
  # actually said, not what we expected it to say.
  LANE_COMMIT="$commit"
  log "health ok: backend /health commit=$commit, lane web answering"
}

bundle_hash() { # content hash of a plugin bundle, node_modules included
  [ -d "$1" ] || { printf 'missing'; return; }
  [ -n "$(find "$1" -type f -print -quit)" ] || { printf 'empty'; return; }
  ( cd "$1" && find . -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1 )
}

resolve_card() { # $1 = sha; prints the issue identifier of the merged PR's card
  local sha="$1" pr ident
  pr="$(gh api "repos/$AUTODEPLOY_REPO/commits/$sha/pulls" 2>/dev/null || printf '[]')"
  # The PR title is the convention ("... (M-169)"); the body is the fallback.
  ident="$(printf '%s' "$pr" | jq -r '[.[0].title // "", .[0].body // ""] | join("\n")' 2>/dev/null \
    | grep -oE 'M-[0-9]+' | head -1 || true)"
  [ -n "$ident" ] || return 1
  printf '%s' "$ident"
}

# ---------------------------------------------------------------------------
# Evidence: latest.md plus the reporter autopilot's webhook
# ---------------------------------------------------------------------------
# This host holds the Owner's CLI token, so a comment posted from here would
# carry the Owner's name. It must not: evidence comments are posted by the
# "lane-z deploy reporter" autopilot under its own agent identity (Architect,
# 2026-10-30). The tick writes latest.md and pokes that autopilot's webhook;
# the URL is a credential, so it lives in a 0600 file — never in the repo, the
# log, or a comment. A webhook that cannot be reached leaves the evidence on
# disk and turns the tick red.

CARD=""

write_report() { # $1 = body file; sets CARD
  local body="$1" out="$AUTODEPLOY_REPORT_DIR/latest.md" tmp
  CARD="$(resolve_card "$SHA" || true)"
  [ -n "$CARD" ] || log "no merged PR with an M-NNN reference for $SHA; latest.md names no card"
  mkdir -p "$AUTODEPLOY_REPORT_DIR"
  tmp="$out.tmp.$$"
  {
    printf 'card: %s\n' "$CARD"
    printf 'sha: %s\n' "$SHA"
    printf 'generated: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'dsh_z_noauth: %s\n' "${DSHZ_SMOKE_LINE:-not-run}"
    printf 'dsh_z_noauth_reapply: %s\n' "${DSHZ_REAPPLY_NOTE:-not-run}"
    printf -- '---\n'
    cat "$body"
  } > "$tmp"
  chmod 0644 "$tmp"
  mv "$tmp" "$out"
  log "evidence written to $out (card ${CARD:-none})"
}

notify_reporter() { # no args; uses CARD. Non-zero means the evidence was not delivered.
  local url attempt=1
  if [ -z "$CARD" ]; then
    log "no card to notify; the evidence stays at $AUTODEPLOY_REPORT_DIR/latest.md"
    return 0
  fi
  if [ ! -s "$AUTODEPLOY_WEBHOOK_FILE" ]; then
    log "no reporter webhook at $AUTODEPLOY_WEBHOOK_FILE; evidence stays at $AUTODEPLOY_REPORT_DIR/latest.md"
    return 1
  fi
  url="$(tr -d '\r\n' < "$AUTODEPLOY_WEBHOOK_FILE")"
  while :; do
    if curl -fsS --max-time 15 -X POST -H 'Content-Type: application/json' \
         -H "Idempotency-Key: autodeploy-${SHA:0:9}-$RUN_ID" \
         -d "{\"card\":\"$CARD\",\"sha\":\"$SHA\"}" "$url" >/dev/null; then
      log "reporter autopilot notified for $CARD"
      return 0
    fi
    if [ "$attempt" -ge 3 ]; then
      log "reporter webhook failed after 3 attempts; evidence stays at $AUTODEPLOY_REPORT_DIR/latest.md"
      return 1
    fi
    attempt=$((attempt + 1))
    sleep 5
  done
}

report() { # $1 = green|red
  # A dry run is not a deploy: it must never leave evidence behind.
  if [ "$DRY_RUN" = 1 ]; then return 0; fi
  local kind="$1" body="$LANE_ROOT/data/autodeploy-comment.md" dur applied plugin_note
  dur=$(( $(date +%s) - STARTED ))
  applied="$( [ -s "$LANE_ROOT/data/autodeploy-migrations.applied" ] && paste -sd', ' "$LANE_ROOT/data/autodeploy-migrations.applied" || true )"
  [ -n "$applied" ] || applied="none"
  plugin_note="$(cat "$LANE_ROOT/data/autodeploy-plugin.note" 2>/dev/null || printf 'unchanged')"
  daemon_note="running $(printf '%s' "$(daemon_health_json)" | jq -r '.cli_commit // "unknown"' 2>/dev/null || printf 'unknown')"
  if [ -f "$(daemon_refresh_pending_file)" ]; then
    daemon_note="refresh pending for $(cat "$(daemon_refresh_pending_file)" 2>/dev/null || printf 'unknown') (deferred, no active-task interruption)"
  fi
  [ -n "$DSHZ_SMOKE_LINE" ] || DSHZ_SMOKE_LINE="not-run"

  if [ "$kind" = green ]; then
    {
      printf '**[autodeploy]** lane z is on `%s`\n\n' "${SHA:0:9}"
      printf -- '- version: `%s` (built %s)\n' "$(release_version)" "${BUILT_AT:-unknown}"
      printf -- '- CI: [run %s](%s), build %s\n' "$RUN_ID" "${RUN_URL:-n/a}" "${BUILD_TIME:-unknown}"
      printf -- '- migrations applied: %s\n' "$applied"
      printf -- '- health: backend `/health` commit `%s`, lane web HTTP up, `moct-be-z /health` ok\n' "${LANE_COMMIT:-$SHA}"
      printf -- '- plugin: %s\n' "$plugin_note"
      printf -- '- daemon: %s\n' "$daemon_note"
      printf -- '- dsh-z noauth: %s (reapply: %s)\n' "$DSHZ_SMOKE_LINE" "$DSHZ_REAPPLY_NOTE"
      printf -- '- DB backup: `%s` (never auto-restored)\n' "${DUMP:-none taken}"
      printf -- '- disk: `/mnt/workspace` %sG free before, %sG after\n' "$DF_BEFORE" "$(free_gb)"
      printf -- '- deploy took %ss\n' "$dur"
    } > "$body"
  else
    {
      printf '**[autodeploy]** lane z deploy of `%s` FAILED — rolled back to `%s`\n\n' "${SHA:0:9}" "$(release_sha)"
      printf -- '- failed in step: `%s` — %s\n' "$STEP" "$FAIL_REASON"
      printf -- '- CI: [run %s](%s)\n' "$RUN_ID" "${RUN_URL:-n/a}"
      printf -- '- migrations applied before the failure: %s\n' "$applied"
      printf -- '- dsh-z noauth: %s (reapply: %s)\n' "${DSHZ_SMOKE_LINE:-not-run}" "${DSHZ_REAPPLY_NOTE:-not-run}"
      printf -- '- DB dump (for a human; NOT restored automatically): `%s`\n' "${DUMP:-none taken}"
      printf -- '- log: `%s`\n' "$LOG_FILE"
    } > "$body"
  fi

  if [ "$DRILL_FAIL_HEALTH" = 1 ]; then
    log "drill: report not delivered. Body follows:"
    sed 's/^/  | /' "$body"
    return 0
  fi
  write_report "$body"
  rm -f "$body"
  # A failed notification never fails the deploy: report() must not hand a
  # healthy lane to the ERR trap and roll it back. It only marks the tick red.
  notify_reporter || REPORT_FAILED=1
  return 0
}

prune_backups() {
  local keep="${AUTODEPLOY_BACKUP_KEEP_COUNT:-10}" running running_prefix dump name rest dump_sha dump_time
  local retained=0
  local -a dumps=()
  if ! [[ "$keep" =~ ^[0-9]+$ ]] || [ "$keep" -lt 1 ]; then
    log "invalid AUTODEPLOY_BACKUP_KEEP_COUNT='$keep'; retaining backups"
    return 0
  fi
  running="$(release_sha)"
  running_prefix="${running:0:9}"
  mapfile -t dumps < <(find "$AUTODEPLOY_BACKUP_DIR" -maxdepth 1 -type f -name 'multica_z-pre-*.dump' \
    -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  for dump in "${dumps[@]}"; do
    name="${dump##*/}"
    if [ -n "$running_prefix" ] && [[ "$name" == "multica_z-pre-${running_prefix}-"*.dump ]]; then
      log "keeping backup for running release: $name"
      if [ "$retained" -lt "$keep" ]; then retained=$((retained + 1)); fi
      continue
    fi
    if [ "$retained" -lt "$keep" ]; then
      retained=$((retained + 1))
      continue
    fi
    if [ "$DRY_RUN" = 1 ]; then
      log "dry-run: would prune old backup $name"
      continue
    fi
    rm -f -- "$dump"
    rest="${name#multica_z-pre-}"
    dump_sha="${rest%%-*}"
    dump_time="${rest#*-}"
    dump_time="${dump_time%.dump}"
    rm -f -- "$AUTODEPLOY_BACKUP_DIR/multica_z-pre-${dump_sha}-migrations-${dump_time}.txt"
    log "pruned old backup $name"
  done
}

# ---------------------------------------------------------------------------
# Lock + guards
# ---------------------------------------------------------------------------

exec 9>"$LANE_ROOT/data/autodeploy.lock"
if ! flock -n 9; then
  log "another deploy holds the lock; skipping this tick"
  exit 0
fi

STARTED=$(date +%s)
DF_BEFORE=$(free_gb)
log "start: lane z on $(release_sha) ($(release_version)), /mnt/workspace ${DF_BEFORE}G free"

step "resolve-target"
if [ -n "$FORCE_SHA" ]; then
  SHA="$FORCE_SHA"
  RUN_ID="$(run_for_sha "$SHA")"
  [ -n "$RUN_ID" ] || fail "no successful push run of $AUTODEPLOY_WORKFLOW on $AUTODEPLOY_BRANCH for $SHA"
  RUN_JSON="$(gh run view "$RUN_ID" --repo "$AUTODEPLOY_REPO" --json headSha,url,createdAt,updatedAt,conclusion)"
else
  # newest_green returns a one-element list; normalise it to the same object
  # shape `gh run view` gives, so everything below reads one shape.
  RUN_JSON="$(newest_green | jq '.[0] // empty')"
  SHA="$(printf '%s' "$RUN_JSON" | jq -r '.headSha // empty')"
  [ -n "$SHA" ] || { log "no successful push run found on $AUTODEPLOY_BRANCH"; exit 0; }
  RUN_ID="$(printf '%s' "$RUN_JSON" | jq -r '.databaseId // empty')"
fi
RUN_URL="$(printf '%s' "$RUN_JSON" | jq -r '.url // empty')"
RUN_STARTED="$(printf '%s' "$RUN_JSON" | jq -r '.createdAt // empty')"
RUN_ENDED="$(printf '%s' "$RUN_JSON" | jq -r '.updatedAt // empty')"
if [ -n "$RUN_STARTED" ] && [ -n "$RUN_ENDED" ]; then
  BUILD_TIME="$(( ( $(date -d "$RUN_ENDED" +%s) - $(date -d "$RUN_STARTED" +%s) ) / 60 ))m$(( ( $(date -d "$RUN_ENDED" +%s) - $(date -d "$RUN_STARTED" +%s) ) % 60 ))s"
else
  BUILD_TIME="unknown"
fi

if [ "$DRY_RUN" = 1 ] && [ "$(release_sha)" = "$SHA" ]; then
  log "dry-run: lane z already runs ${SHA:0:9} (run $RUN_ID); a real tick exits here"
  exit 0
fi

if [ "$(release_sha)" = "$SHA" ]; then
  log "lane z already runs $SHA; backend is current"
  # M-301: a previous deploy may have deferred the daemon refresh because a
  # task was running. Complete it now (no interruption) so the daemon advances
  # to the deployed release without a manual restart.
  if daemon_runs_release "$(daemon_health_json)" "$SHA" && [ ! -f "$(daemon_refresh_pending_file)" ]; then
    log "daemon z already running ${SHA:0:9}; nothing to do"
    # M-406: verify-only on routine ticks; repair (reapply+restart+recheck) if broken.
    step "dsh-z-noauth"
    if ! dshz_verify_or_repair; then
      SKIP_BACKEND_ROLLBACK=1
      fail "dsh-z noauth smoke failed after repair attempt: ${DSHZ_SMOKE_LINE:-unknown} (reapply=${DSHZ_REAPPLY_NOTE:-unknown})"
    fi
    exit 0
  fi
  step "daemon-refresh"
  refresh_lane_daemon_after_deploy "$SHA" || exit 1
  # M-406: verify-only on routine ticks; repair if broken.
  step "dsh-z-noauth"
  if ! dshz_verify_or_repair; then
    SKIP_BACKEND_ROLLBACK=1
    fail "dsh-z noauth smoke failed after repair attempt: ${DSHZ_SMOKE_LINE:-unknown} (reapply=${DSHZ_REAPPLY_NOTE:-unknown})"
  fi
  log "daemon refresh complete for ${SHA:0:9}"
  exit 0
fi

if [ -f "$LANE_ROOT/data/autodeploy.failed" ] && [ "$(cat "$LANE_ROOT/data/autodeploy.failed")" = "$SHA" ]; then
  log "$SHA already failed on this host; not retrying (rm $LANE_ROOT/data/autodeploy.failed to allow it)"
  exit 0
fi

RUNNING_SHA="$(release_sha)"
if [ -n "$RUNNING_SHA" ] && [ "$RUNNING_SHA" != "none" ]; then
  if ! COMPARE_JSON="$(gh api "repos/$AUTODEPLOY_REPO/compare/$RUNNING_SHA...$SHA" 2>/dev/null)"; then
    log "could not compare target $SHA with running release $RUNNING_SHA; skipping"
    exit 0
  fi
  COMPARE_STATUS="$(printf '%s' "$COMPARE_JSON" | jq -r '.status // empty' 2>/dev/null || true)"
  case "$COMPARE_STATUS" in
    ahead|identical) ;;
    behind|diverged)
      log "target is behind running release; skipping"
      exit 0
      ;;
    *)
      log "target is behind running release; skipping (compare status: ${COMPARE_STATUS:-unknown})"
      exit 0
      ;;
  esac
fi

ART="$LANE_ROOT/artifacts/$SHA"
if [ "$DRY_RUN" = 1 ]; then
  log "dry-run: newest green run on $AUTODEPLOY_BRANCH is ${SHA:0:9} (run $RUN_ID, $RUN_URL)"
  log "dry-run: lane z runs $(release_sha) ($(release_version))"
  log "dry-run: artifacts in run $RUN_ID:"
  gh api "repos/$AUTODEPLOY_REPO/actions/runs/$RUN_ID/artifacts" \
    --jq '.artifacts[] | "  - \(.name) \(.size_in_bytes / 1048576 | floor) MB"' 2>/dev/null || log "  (artifact list unavailable)"
  log "dry-run: would download to $ART, back up the DB to $AUTODEPLOY_BACKUP_DIR, install releases/${SHA:0:9}, migrate, switch current, restart $MULTICA_BE_Z_UNIT,"
  log "dry-run: would sync the plugin bundle and restart $DSH_Z_WEB_UNIT only if its content changed, then assert health and comment on $(resolve_card "$SHA" || printf 'no card')"
  log "dry-run: disk ${DF_BEFORE}G free (refuse below ${AUTODEPLOY_MIN_FREE_GB}G), keeping $AUTODEPLOY_KEEP_RELEASES releases"
  prune_backups
  exit 0
fi

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------

step "disk-guard"
if [ "$DF_BEFORE" -lt "$AUTODEPLOY_MIN_FREE_GB" ]; then
  fail "/mnt/workspace has ${DF_BEFORE}G free, below the ${AUTODEPLOY_MIN_FREE_GB}G floor; refusing to download"
fi

step "download"
PREV_REL="$(readlink -f "$LANE_ROOT/multica-be/current" 2>/dev/null || true)"
rm -rf "$ART"; mkdir -p "$ART/multica-be" "$ART/web-dist"
log "downloading multica-be-$SHA"
gh run download "$RUN_ID" --repo "$AUTODEPLOY_REPO" -n "multica-be-$SHA" -D "$ART/multica-be"
log "downloading web-dist-$SHA"
gh run download "$RUN_ID" --repo "$AUTODEPLOY_REPO" -n "web-dist-$SHA" -D "$ART/web-dist"
# The plugin bundle is optional: a push that touches nothing under packages/
# never runs the frontend job, and then the lane's current bundle is correct.
PLUGIN_PRESENT=0
if gh run download "$RUN_ID" --repo "$AUTODEPLOY_REPO" -n "dsh-plugin-$SHA" -D "$ART/dsh-plugin" 2>/dev/null; then
  PLUGIN_PRESENT=1
fi

step "verify-artifact"
[ -x "$ART/multica-be/bin/server" ] || fail "multica-be artifact has no bin/server"
[ -d "$ART/multica-be/migrations" ] || fail "multica-be artifact has no migrations/"
ART_SHA="$(jq -r '.sha // empty' "$ART/multica-be/release.json")"
[ "$ART_SHA" = "$SHA" ] || fail "artifact release.json sha '$ART_SHA' != run sha '$SHA'"
BUILT_AT="$(jq -r '.builtAt // "unknown"' "$ART/multica-be/release.json")"
log "artifact verified: sha $SHA, built $BUILT_AT"

step "supersede-check"
if [ -z "$FORCE_SHA" ]; then
  NOW_NEWEST="$(newest_green | jq -r '.[0].headSha // empty')"
  if [ "$NOW_NEWEST" != "$SHA" ]; then
    log "a newer green SHA ($NOW_NEWEST) landed while downloading; dropping $SHA for the next tick"
    rm -rf "$ART"
    exit 0
  fi
fi

step "backup"
# M-399 repair: the lane's data lives where the running backend points, not
# where dsh-z.env's legacy MULTICA_Z_* DB knobs say. The backend unit's
# EnvironmentFile (multica-be/env) is the source of truth for DATABASE_URL —
# today 127.0.0.1:55434/multica_mmx on the multica-pg17-mmx container. The old
# code dumped/migrated the infra-postgres multica_z DB instead, so migrations
# were skipped as "already applied" against the wrong cluster and the live
# DB stayed behind, failing the post-switch health check. Parse DATABASE_URL
# once here; fail loudly rather than fall back to the wrong DB.
BACKEND_ENV_FILE="$LANE_ROOT/multica-be/env"
[ -f "$BACKEND_ENV_FILE" ] || fail "no backend env file at $BACKEND_ENV_FILE"
LIVE_DSN="$(sed -n 's/^DATABASE_URL=//p' "$BACKEND_ENV_FILE" | tail -1)"
[ -n "$LIVE_DSN" ] || fail "no DATABASE_URL in $BACKEND_ENV_FILE"
LIVE_URL_BODY="${LIVE_DSN#postgres://}"; LIVE_URL_BODY="${LIVE_URL_BODY%%\?*}"
LIVE_AUTH="${LIVE_URL_BODY%@*}"; LIVE_HOSTPORT="${LIVE_URL_BODY#*@}"
LIVE_DB_USER="${LIVE_AUTH%%:*}"; LIVE_DB_PASS="${LIVE_AUTH#*:}"
LIVE_DB_HOST="${LIVE_HOSTPORT%%:*}"; LIVE_HOSTDB="${LIVE_HOSTPORT#*:}"
LIVE_DB_PORT="${LIVE_HOSTDB%%/*}"; LIVE_DB_NAME="${LIVE_HOSTDB#*/}"
# The host has no psql/pg_dump; the client lives inside the podman container
# that publishes the DB port. Resolve it by port so a container rename is not
# hardcoded here.
# consume podman's full output: an early awk `exit` closes the pipe and
# SIGPIPEs podman, which `set -o pipefail` reports as a backup failure.
LIVE_DB_CONTAINER="$(podman ps --format '{{.Names}}\t{{.Ports}}' 2>/dev/null \
  | awk -F'\t' -v p=":$LIVE_DB_PORT->" '$2 ~ p && !found {print $1; found=1}')"
[ -n "$LIVE_DB_CONTAINER" ] || fail "no podman container publishes port $LIVE_DB_PORT for the live DB"
LIVE_DB_TOOL() { podman exec "$LIVE_DB_CONTAINER" "$@"; }
export PGPASSWORD="$LIVE_DB_PASS"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
DUMP="$AUTODEPLOY_BACKUP_DIR/multica_z-pre-${SHA:0:9}-$TS.dump"
MIG_LIST_PRE="$AUTODEPLOY_BACKUP_DIR/multica_z-pre-${SHA:0:9}-migrations-$TS.txt"
log "dumping live DB $LIVE_DB_NAME @ $LIVE_DB_HOST:$LIVE_DB_PORT (container $LIVE_DB_CONTAINER) to $DUMP"
LIVE_DB_TOOL pg_dump -U "$LIVE_DB_USER" -Fc "$LIVE_DB_NAME" > "$DUMP"
[ -s "$DUMP" ] || fail "pg_dump produced an empty file"
LIVE_DB_TOOL psql -U "$LIVE_DB_USER" -d "$LIVE_DB_NAME" -tAc \
  'select version from schema_migrations order by version' | sed '/^$/d' | sort > "$MIG_LIST_PRE"
log "backup ok: $(du -h "$DUMP" | cut -f1), $(wc -l < "$MIG_LIST_PRE") migrations recorded"

step "install-release"
REL="$LANE_ROOT/multica-be/releases/$SHA"
rm -rf "$REL"; mkdir -p "$REL"
cp -a "$ART/multica-be/." "$REL/"
: > "$REL/.complete"
printf '%s\n' "$PREV_REL" > "$LANE_ROOT/multica-be/previous-release"
log "release installed at $REL"

step "migrate"
# M-399 repair: migrate the same live DB the backup above was taken from
# (DATABASE_URL from multica-be/env), never the legacy MULTICA_Z_* knobs.
( cd "$REL" && DATABASE_URL="$LIVE_DSN" "$REL/bin/migrate" up )
LIVE_DB_TOOL psql -U "$LIVE_DB_USER" -d "$LIVE_DB_NAME" -tAc \
  'select version from schema_migrations order by version' | sed '/^$/d' | sort > "$LANE_ROOT/data/autodeploy-migrations.post"
comm -13 "$MIG_LIST_PRE" "$LANE_ROOT/data/autodeploy-migrations.post" > "$LANE_ROOT/data/autodeploy-migrations.applied" || true
log "migrations applied: $( [ -s "$LANE_ROOT/data/autodeploy-migrations.applied" ] && paste -sd', ' "$LANE_ROOT/data/autodeploy-migrations.applied" || echo none)"

step "switch"
# The flip is what makes this a switch, so the rollback marker goes on first.
SWITCHED=1
render_backend_unit "$REL" || fail "could not point $MULTICA_BE_Z_UNIT at releases/${SHA:0:9}"
[ "$(readlink -f "$LANE_ROOT/multica-be/current")" = "$REL" ] || fail "current does not resolve to $REL"
UNIT_REL="$(unit_release)"
[ "$UNIT_REL" = "$LANE_ROOT/multica-be/current" ] \
  || fail "$MULTICA_BE_Z_UNIT runs '$UNIT_REL', not the current symlink"
log "$MULTICA_BE_Z_UNIT -> current -> releases/${SHA:0:9}; restarting"
systemctl --user restart "$MULTICA_BE_Z_UNIT"

step "health"
assert_health "$SHA" || fail "health check failed after the switch"

step "daemon-refresh"
# M-301: ordinary autodeploy must refresh the z daemon so session logic becomes
# active on the deployed build. Safe: never interrupts an active task (defers
# and retries). A deferral is not a deploy failure — the lane backend is healthy
# and the daemon's auto-reload/M-232 recovery handles the eventual restart.
if ! refresh_lane_daemon_after_deploy "$SHA"; then
  health="$(daemon_health_json)"
  if daemon_is_idle "$health"; then
    fail "daemon refresh did not reach ${SHA:0:9} after restart"
  fi
  log "daemon refresh deferred (busy); deploy remains healthy, will retry next tick"
fi

step "plugin"
if [ "$PLUGIN_PRESENT" = 1 ]; then
  NEW_HASH="$(bundle_hash "$ART/dsh-plugin")"
  OLD_HASH="$(bundle_hash "$DSH_Z_PLUGIN_BUNDLE")"
  if [ "$NEW_HASH" = "$OLD_HASH" ]; then
    printf 'unchanged (artifact matches the installed bundle)\n' > "$LANE_ROOT/data/autodeploy-plugin.note"
    log "plugin bundle unchanged; $DSH_Z_WEB_UNIT not restarted"
    # M-406: verify-only on unchanged ticks (no restart). If broken, repair once
    # (reapply+restart+recheck) — restart is allowed as repair, not routine.
    step "dsh-z-noauth"
    if ! dshz_verify_or_repair; then
      SKIP_BACKEND_ROLLBACK=1
      fail "dsh-z noauth smoke failed after repair attempt: ${DSHZ_SMOKE_LINE:-unknown} (reapply=${DSHZ_REAPPLY_NOTE:-unknown})"
    fi
  else
    log "plugin bundle changed ($OLD_HASH -> $NEW_HASH); installing (reapply before restart)"
    rsync -a --delete "$ART/dsh-plugin/" "$DSH_Z_PLUGIN_BUNDLE/"
    # M-406: reapply/verify files before web restart on plugin-update path.
    guard="$(dshz_guard)"
    if [ -x "$guard" ]; then
      if out="$(DSH_Z_WEB_URL="$(resolve_lane_web_url)" DSH_Z_GATEWAY_URL="${DSH_Z_GATEWAY_URL:-http://127.0.0.1:3738}" "$guard" reapply-files 2>&1)"; then
        DSHZ_REAPPLY_NOTE="$(printf '%s\n' "$out" | tail -1)"
        log "dsh-z noauth pre-restart: $DSHZ_REAPPLY_NOTE"
      else
        SKIP_BACKEND_ROLLBACK=1
        fail "dsh-z noauth reapply before restart failed: $(printf '%s\n' "$out" | tail -1)"
      fi
    else
      DSHZ_REAPPLY_NOTE="missing-guard"
    fi
    printf 'updated (dsh-z restarted)\n' > "$LANE_ROOT/data/autodeploy-plugin.note"
    log "restarting $DSH_Z_WEB_UNIT (bundle changed)"
    systemctl --user restart "$DSH_Z_WEB_UNIT"
    web_liveness "$(resolve_lane_web_url)" 90 || fail "lane web did not come back after the plugin update"
    assert_health "$SHA" || fail "health check failed after the plugin update"
    step "dsh-z-noauth"
    if ! dshz_smoke_record >/dev/null; then
      # try reapply once, restart, recheck (web-profile failure policy)
      if ! dshz_repair_once; then
        SKIP_BACKEND_ROLLBACK=1
        fail "dsh-z noauth smoke failed after plugin update+repair: ${DSHZ_SMOKE_LINE:-unknown} (reapply=${DSHZ_REAPPLY_NOTE:-unknown})"
      fi
    fi
  fi
else
  printf 'unchanged (no dsh-plugin artifact in this run)\n' > "$LANE_ROOT/data/autodeploy-plugin.note"
  log "no dsh-plugin artifact in run $RUN_ID; keeping the installed bundle"
  # M-406: post-deploy verification on deploy ticks with no plugin artifact.
  step "dsh-z-noauth"
  if ! dshz_verify_or_repair; then
    SKIP_BACKEND_ROLLBACK=1
    fail "dsh-z noauth smoke failed after repair attempt: ${DSHZ_SMOKE_LINE:-unknown} (reapply=${DSHZ_REAPPLY_NOTE:-unknown})"
  fi
fi

# pnpm can replace the published sidebar-right package during profile repair.
# Reapply the lane's checked-out fork on every deploy tick; restart the web only
# when package contents changed. Backup and rollback details live in lane-lib.
step "sidebar-right-profile"
if ensure_sidebar_right_profile; then
  if [ "${SIDEBAR_RIGHT_PROFILE_CHANGED:-0}" = 1 ]; then
    systemctl --user restart "$DSH_Z_WEB_UNIT"
    wait_http "$(resolve_lane_web_url)/" 90 '' || fail "lane web did not come back after sidebar-right repair"
    assert_health "$SHA" || fail "health check failed after sidebar-right repair"
  fi
else
  fail "could not restore the sidebar-right fork in the web profile"
fi

step "cleanup"
rm -rf "$ART"
prune_releases
prune_backups
rm -f "$LANE_ROOT/data/autodeploy.failed"

step "report"
report green
log "done in $(( $(date +%s) - STARTED ))s; /mnt/workspace $(free_gb)G free (was ${DF_BEFORE}G)"
if [ "$REPORT_FAILED" = 1 ]; then
  # The lane is healthy, so this must not roll back — but a deploy nobody was
  # told about is a failed tick, and the unit status is how that surfaces.
  log "deploy is UP but the evidence comment was not delivered; latest.md holds it"
  exit 1
fi
