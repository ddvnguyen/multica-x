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
  if [ "$SWITCHED" = 1 ]; then
    log "rolling back to the previous release"
    bash "$SCRIPT_DIR/rollback.sh" || log "ROLLBACK FAILED — lane z may be on the broken release"
  else
    rm -rf "$LANE_ROOT/multica-be/releases/$SHA.tmp.$$" "$LANE_ROOT/mmx-pwa/releases/$SHA.tmp.$$" "$LANE_ROOT/mmx-pwa/.converge-"*
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

assert_mmx_health() { # $1 = sha the PWA must serve (full sha; sw.js carries it)
  local sha="$1" manifest port="${MMX_PWA_Z_PORT:?MMX_PWA_Z_PORT is not set}"
  manifest="$(curl -fsS --max-time 10 "http://127.0.0.1:$port/manifest.webmanifest")" \
    || { log "mmx-pwa z manifest did not answer"; return 1; }
  printf '%s' "$manifest" | grep -q '"name":"MMX"' \
    || { log "mmx-pwa z manifest is not the MMX brand"; return 1; }
  curl -fsS --max-time 10 "http://127.0.0.1:$port/sw.js" | grep -q "$sha" \
    || { log "mmx-pwa z sw.js is not stamped with $sha"; return 1; }
  log "mmx-pwa health ok: brand MMX, sw.js stamped $sha"
}
converge_mmx_pwa() { # $1 = sha, $2 = run id. PWA-only repair for a backend-current lane.
  # Stages the CI mmx-pwa artifact to a temp dir (never the live release dir),
  # verifies brand + stamp there, then swaps mmx-pwa/current and restarts only
  # the PWA unit. Backend release, backend current, migrations, backend unit,
  # daemon, plugin, and backups are untouched.
  #
  # Post-switch failures restore the EXACT prior PWA state: symlink, marker,
  # env file, unit restart, and a health recheck against the prior SHA —
  # SWITCHED stays 0 (the backend never moved), so on_error cannot roll this
  # back; converge owns its own undo. A half-downloaded stage dir is removed;
  # $stage_rel itself is only installed via promote_staged (never merged
  # into an existing directory).
  local sha="$1" run_id="$2" stage stage_rel live_rel live_marker live_env
  [ -n "${MMX_PWA_Z_PORT:-}" ] || { log "converge: MMX_PWA_Z_PORT is not set"; return 1; }
  [ -n "${MMX_PWA_Z_UNIT:-}" ] || { log "converge: MMX_PWA_Z_UNIT is not set"; return 1; }
  stage="$(mktemp -d "$LANE_ROOT/mmx-pwa/.converge-XXXXXX")" || return 1
  if ! gh run download "$run_id" --repo "$AUTODEPLOY_REPO" -n "mmx-pwa-$sha" -D "$stage"; then
    log "converge: mmx-pwa artifact unavailable for $sha (run $run_id)"
    rm -rf "$stage"
    return 1
  fi
  if [ "$(jq -r '.sha // empty' "$stage/release.json" 2>/dev/null || true)" != "$sha" ]; then
    log "converge: staged mmx-pwa release.json sha mismatch for $sha"
    rm -rf "$stage"
    return 1
  fi
  if [ ! -f "$stage/standalone/apps/web/server.js" ] \
    || ! grep -rq 'mmx-install' "$stage/standalone" 2>/dev/null \
    || ! grep -q "$sha" "$stage/standalone/apps/web/public/sw.js" 2>/dev/null; then
    log "converge: staged mmx-pwa tree failed brand/stamp verification for $sha"
    rm -rf "$stage"
    return 1
  fi
  stage_rel="$LANE_ROOT/mmx-pwa/releases/$sha"
  live_rel="$(readlink -f "$LANE_ROOT/mmx-pwa/current" 2>/dev/null || true)"
  live_marker="$(cat "$LANE_ROOT/mmx-pwa/previous-release" 2>/dev/null || true)"
  live_env="$(cat "$LANE_ROOT/mmx-pwa/env" 2>/dev/null || true)"
  rm -rf "$stage_rel.tmp.$$"; mkdir -p "$stage_rel.tmp.$$"
  cp -a "$stage/standalone/." "$stage_rel.tmp.$$/"
  rm -rf "$stage"
  if [ ! -f "$stage_rel.tmp.$$/apps/web/server.js" ] \
    || ! grep -rq 'mmx-install' "$stage_rel.tmp.$$/apps/web/.next/" 2>/dev/null; then
    log "converge: staged copy failed final verification for $sha"
    rm -rf "$stage_rel.tmp.$$"
    return 1
  fi
  promote_staged "$stage_rel" "$stage_rel.tmp.$$" || return 1
  : > "$stage_rel/.complete"
  # --- point of no return for the PWA symlink; every step below can fail ---
  ln -sfn "$stage_rel" "$LANE_ROOT/mmx-pwa/current"
  if [ "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" != "$stage_rel" ]; then
    log "converge: mmx-pwa current does not resolve to $stage_rel"
    converge_restore_pwa "$live_rel" "$live_marker" "$live_env" "$sha"
    return 1
  fi
  printf '%s\n' "$live_rel" > "$LANE_ROOT/mmx-pwa/previous-release"
  if ! write_mmx_pwa_env "$sha"; then
    converge_restore_pwa "$live_rel" "$live_marker" "$live_env" "$sha"
    return 1
  fi
  if ! systemctl --user restart "${MMX_PWA_Z_UNIT:?MMX_PWA_Z_UNIT is not set}"; then
    log "converge: mmx-pwa unit restart failed for ${sha:0:9}"
    converge_restore_pwa "$live_rel" "$live_marker" "$live_env" "$sha"
    return 1
  fi
  if ! assert_mmx_health "$sha"; then
    converge_restore_pwa "$live_rel" "$live_marker" "$live_env" "$sha"
    return 1
  fi
  printf 'converged (mmx-pwa-z restarted to %s; backend untouched)\n' "${sha:0:9}" > "$LANE_ROOT/data/autodeploy-mmx-pwa.note"
  log "mmx-pwa z converged to releases/${sha:0:9}; backend untouched"
}

converge_restore_pwa() { # $1 = prior current target, $2 = prior marker, $3 = prior env, $4 = failed sha
  # Best-effort exact-state restore after a failed converge switch. Every
  # step is guarded: a restore that cannot complete is LOUD, because the
  # lane is then serving an unknown PWA state with SWITCHED=0.
  local prior_rel="$1" prior_marker="$2" prior_env="$3" failed_sha="$4" prior_sha
  log "converge: restoring prior mmx-pwa state after failure on ${failed_sha:0:9}"
  if [ -n "$prior_rel" ] && [ -d "$prior_rel" ]; then
    ln -sfn "$prior_rel" "$LANE_ROOT/mmx-pwa/current"
    [ "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$prior_rel" ] \
      || log "converge RESTORE FAILED: mmx-pwa current does not resolve to $prior_rel"
  else
    log "converge RESTORE FAILED: prior PWA release '$prior_rel' is gone; current left on the failed tree for inspection"
  fi
  if [ -n "$prior_marker" ]; then
    printf '%s\n' "$prior_marker" > "$LANE_ROOT/mmx-pwa/previous-release"
  else
    rm -f "$LANE_ROOT/mmx-pwa/previous-release"
  fi
  if [ -n "$prior_env" ]; then
    printf '%s' "$prior_env" > "$LANE_ROOT/mmx-pwa/env.tmp.$$"
    mv -f "$LANE_ROOT/mmx-pwa/env.tmp.$$" "$LANE_ROOT/mmx-pwa/env" \
      || log "converge RESTORE FAILED: could not restore mmx-pwa env"
  fi
  systemctl --user restart "${MMX_PWA_Z_UNIT:?MMX_PWA_Z_UNIT is not set}" \
    || log "converge RESTORE FAILED: mmx-pwa unit did not restart"
  if [ -n "$prior_rel" ] && [ -f "$prior_rel/apps/web/public/sw.js" ]; then
    prior_sha="$(grep -oE '[0-9a-f]{40}' "$prior_rel/apps/web/public/sw.js" | head -1 || true)"
  fi
  if [ -n "${prior_sha:-}" ]; then
    assert_mmx_health "$prior_sha" \
      && log "converge: prior mmx-pwa state restored and healthy (${prior_sha:0:9})" \
      || log "converge RESTORE FAILED: prior mmx-pwa state did not come back healthy"
  else
    wait_http "http://127.0.0.1:${MMX_PWA_Z_PORT:?MMX_PWA_Z_PORT is not set}/manifest.webmanifest" 60 'MMX' \
      && log "converge: prior mmx-pwa unit is serving again (stamp unknown, pre-M-451 tree)" \
      || log "converge RESTORE FAILED: prior mmx-pwa unit is not serving"
  fi
}

write_mmx_pwa_env() { # $1 = sha for NEXT_PUBLIC_APP_VERSION
  # Writes to a temp file and atomically moves it over the live env, so a
  # failed write never leaves a half-written env behind.
  if [ -f "$LANE_RUNTIME_ENV" ]; then . "$LANE_RUNTIME_ENV"; fi
  [ -n "${MULTICA_Z_DEV_CODE:-}" ] || { log "no MULTICA_Z_DEV_CODE in $LANE_RUNTIME_ENV"; return 1; }
  local tmp="$LANE_ROOT/mmx-pwa/env.tmp.$$"
  umask 077
  {
    echo "PORT=${MMX_PWA_Z_PORT:?MMX_PWA_Z_PORT is not set}"
    echo "HOSTNAME=127.0.0.1"
    echo "MMX_BIND_HOST=127.0.0.1"
    echo "REMOTE_API_URL=http://127.0.0.1:${MULTICA_BE_Z_PORT:?MULTICA_BE_Z_PORT is not set}"
    echo "NEXT_PUBLIC_APP_VERSION=$1"
    echo "MMX_AUTH_BYPASS=${MMX_AUTH_BYPASS:-1}"
    echo "MULTICA_DEV_VERIFICATION_CODE=${MULTICA_Z_DEV_CODE:?MULTICA_Z_DEV_CODE is not set}"
  } > "$tmp"
  umask 022
  mv -f "$tmp" "$LANE_ROOT/mmx-pwa/env" || { log "could not install mmx-pwa env"; rm -f "$tmp"; return 1; }
}
promote_staged() { # $1 = live release dir, $2 = staged dir ($1.tmp.$$)
  # Replace $1 with $2 without ever merging into an existing directory.
  # Plain `mv $2 $1` moves $2 INSIDE $1 when $1 exists — a same-SHA retry,
  # a stale/broken release, or a rollback-then-redeploy would then serve
  # old contents under a fresh marker. Instead the existing $1 (if any) is
  # moved aside first; a failed promotion restores it, so the live tree is
  # either the complete staged tree or byte-identical to before.
  # Callers must guarantee $1 is not the CURRENT release dir of a running
  # unit (the switch step only promotes the non-running target SHA; converge
  # only promotes the non-current PWA SHA).
  local dst="$1" src="$2" aside
  [ -n "$dst" ] && [ -n "$src" ] && [ -d "$src" ] || { log "promote_staged: bad args '$dst' '$src'"; return 1; }
  aside="$dst.aside.$$"
  rm -rf "$aside"
  if [ -d "$dst" ]; then
    mv "$dst" "$aside" || { log "promote_staged: cannot move aside $dst"; return 1; }
  fi
  if mv "$src" "$dst"; then
    rm -rf "$aside"
    return 0
  fi
  log "promote_staged: promotion of $src to $dst failed; restoring"
  rm -rf "$dst"
  if [ -d "$aside" ]; then
    mv "$aside" "$dst" || { log "promote_staged: RESTORE FAILED for $dst (aside at $aside)"; return 1; }
  fi
  return 1
}
assert_health() { # $1 = sha the lane must report
  local sha="$1" commit
  if [ "$DRILL_FAIL_HEALTH" = 1 ]; then
    log "drill: forcing the post-switch health assertion to fail"
    return 1
  fi
  wait_http "http://127.0.0.1:$MULTICA_BE_Z_PORT/healthz" 120 '"status":"ok"' \
    || { log "multica-be z /healthz did not answer ok"; return 1; }
  # The lane web binds LANE_WEB_HOST (10.88.0.1, podman bridge), not loopback.
  web="$(resolve_lane_web_url)"
  wait_http "$web/multica/healthz" 60 '' \
    || { log "lane web /multica/healthz did not answer"; return 1; }
  wait_http "$web/moct/status" 60 '' \
    || { log "lane web /moct/status did not answer"; return 1; }
  commit="$(curl -fsS --max-time 5 "$web/multica/health" | jq -r '.commit // empty')" \
    || { log "lane web /multica/health did not answer"; return 1; }
  commit_matches "$commit" "$sha" \
    || { log "/multica/health reports commit '$commit', expected '$sha'"; return 1; }
  # CI-built releases answer with the short sha; the report quotes what the lane
  # actually said, not what we expected it to say.
  LANE_COMMIT="$commit"
  log "health ok: /multica/health commit=$commit"
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
  local kind="$1" body="$LANE_ROOT/data/autodeploy-comment.md" dur applied plugin_note mmx_note
  dur=$(( $(date +%s) - STARTED ))
  applied="$( [ -s "$LANE_ROOT/data/autodeploy-migrations.applied" ] && paste -sd', ' "$LANE_ROOT/data/autodeploy-migrations.applied" || true )"
  [ -n "$applied" ] || applied="none"
  plugin_note="$(cat "$LANE_ROOT/data/autodeploy-plugin.note" 2>/dev/null || printf 'unchanged')"
  mmx_note="$(cat "$LANE_ROOT/data/autodeploy-mmx-pwa.note" 2>/dev/null || printf 'unchanged')"
  daemon_note="running $(printf '%s' "$(daemon_health_json)" | jq -r '.cli_commit // "unknown"' 2>/dev/null || printf 'unknown')"
  if [ -f "$(daemon_refresh_pending_file)" ]; then
    daemon_note="refresh pending for $(cat "$(daemon_refresh_pending_file)" 2>/dev/null || printf 'unknown') (deferred, no active-task interruption)"
  fi

  if [ "$kind" = green ]; then
    {
      printf '**[autodeploy]** lane z is on `%s`\n\n' "${SHA:0:9}"
      printf -- '- version: `%s` (built %s)\n' "$(release_version)" "${BUILT_AT:-unknown}"
      printf -- '- CI: [run %s](%s), build %s\n' "$RUN_ID" "${RUN_URL:-n/a}" "${BUILD_TIME:-unknown}"
      printf -- '- migrations applied: %s\n' "$applied"
      printf -- '- health: `/multica/health` commit `%s`, `/multica/healthz` ok, `/moct/status` ok\n' "${LANE_COMMIT:-$SHA}"
      printf -- '- mmx-pwa: %s (manifest MMX, sw.js stamped `%s`)\n' "$mmx_note" "${SHA:0:9}"
      printf -- '- plugin: %s\n' "$plugin_note"
      printf -- '- daemon: %s\n' "$daemon_note"
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
  # M-451: backend-current does not mean PWA-current (pre-fix deploys never
  # installed the PWA). Converge the PWA without touching the live backend
  # release, current symlinks, migrations, or restarts: stage the artifact
  # to a temp dir, verify brand + stamp, swap mmx-pwa/current, restart only
  # the PWA unit. Any failure leaves both `current` symlinks and both
  # previous-release markers exactly as they were.
  if [ -n "${MMX_PWA_Z_PORT:-}" ] && [ -n "${MMX_PWA_Z_UNIT:-}" ] \
    && ! assert_mmx_health "$SHA" 2>/dev/null; then
    step "converge-mmx-pwa"
    converge_mmx_pwa "$SHA" "$RUN_ID" || fail "mmx-pwa convergence failed"
  else
    log "lane z already runs $SHA; backend is current"
  fi
  # M-301: a previous deploy may have deferred the daemon refresh because a
  # task was running. Complete it now (no interruption) so the daemon advances
  # to the deployed release without a manual restart.
  if daemon_runs_release "$(daemon_health_json)" "$SHA" && [ ! -f "$(daemon_refresh_pending_file)" ]; then
    log "daemon z already running ${SHA:0:9}; nothing to do"
    exit 0
  fi
  step "daemon-refresh"
  refresh_lane_daemon_after_deploy "$SHA" || exit 1
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
  log "dry-run: would install mmx-pwa-$SHA to mmx-pwa/releases/${SHA:0:9}, flip mmx-pwa/current, restart ${MMX_PWA_Z_UNIT:-mmx-pwa-z.service}, and assert the MMX brand + sw.js stamp,"
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
PREV_MMX_REL="$(readlink -f "$LANE_ROOT/mmx-pwa/current" 2>/dev/null || true)"
rm -rf "$ART"; mkdir -p "$ART/multica-be" "$ART/mmx-pwa"
log "downloading multica-be-$SHA"
gh run download "$RUN_ID" --repo "$AUTODEPLOY_REPO" -n "multica-be-$SHA" -D "$ART/multica-be"
log "downloading mmx-pwa-$SHA"
gh run download "$RUN_ID" --repo "$AUTODEPLOY_REPO" -n "mmx-pwa-$SHA" -D "$ART/mmx-pwa"
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
MMX_ART_SHA="$(jq -r '.sha // empty' "$ART/mmx-pwa/release.json" 2>/dev/null || true)"
[ "$MMX_ART_SHA" = "$SHA" ] || fail "mmx-pwa artifact release.json sha '$MMX_ART_SHA' != run sha '$SHA'"
[ -f "$ART/mmx-pwa/standalone/apps/web/server.js" ] || fail "mmx-pwa artifact has no standalone/apps/web/server.js"
grep -rq 'mmx-install' "$ART/mmx-pwa/standalone" || fail "mmx-pwa artifact is not the MMX brand build (no mmx-install marker)"
grep -q "$SHA" "$ART/mmx-pwa/standalone/apps/web/public/sw.js" 2>/dev/null || fail "mmx-pwa artifact sw.js is not stamped with $SHA"
log "mmx-pwa artifact verified: sha $SHA, MMX brand, sw.js stamped"
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
TS="$(date -u +%Y%m%dT%H%M%SZ)"
DUMP="$AUTODEPLOY_BACKUP_DIR/multica_z-pre-${SHA:0:9}-$TS.dump"
MIG_LIST_PRE="$AUTODEPLOY_BACKUP_DIR/multica_z-pre-${SHA:0:9}-migrations-$TS.txt"
log "dumping $MULTICA_Z_DB_NAME to $DUMP"
podman exec "$MULTICA_Z_DB_CONTAINER" pg_dump -U hydra -Fc "$MULTICA_Z_DB_NAME" > "$DUMP"
[ -s "$DUMP" ] || fail "pg_dump produced an empty file"
podman exec "$MULTICA_Z_DB_CONTAINER" psql -U hydra -d "$MULTICA_Z_DB_NAME" -tAc \
  'select version from schema_migrations order by version' | sed '/^$/d' | sort > "$MIG_LIST_PRE"
log "backup ok: $(du -h "$DUMP" | cut -f1), $(wc -l < "$MIG_LIST_PRE") migrations recorded"

step "install-release"
# Stage to a temp dir, never the live release dir: a failed copy or failed
# verification must leave the running release (and the previous SHA's dir)
# byte-identical. The temp dir becomes the release only at the switch.
REL="$LANE_ROOT/multica-be/releases/$SHA"
rm -rf "$REL.tmp.$$"; mkdir -p "$REL.tmp.$$"
cp -a "$ART/multica-be/." "$REL.tmp.$$"
[ -x "$REL.tmp.$$/bin/server" ] || fail "staged backend release has no bin/server"
[ -f "$REL.tmp.$$/release.json" ] || fail "staged backend release has no release.json"
: > "$REL.tmp.$$/.complete"
log "backend release staged at $REL.tmp.$$"
step "install-mmx-pwa"
# Same staging rule as the backend release (M-451 comment above).
MMX_REL="$LANE_ROOT/mmx-pwa/releases/$SHA"
rm -rf "$MMX_REL.tmp.$$"; mkdir -p "$MMX_REL.tmp.$$"
cp -a "$ART/mmx-pwa/standalone/." "$MMX_REL.tmp.$$"
[ -f "$MMX_REL.tmp.$$/apps/web/server.js" ] || fail "staged mmx-pwa release has no apps/web/server.js"
grep -rq 'mmx-install' "$MMX_REL.tmp.$$/apps/web/.next/" || fail "staged mmx-pwa release lost its MMX brand"
: > "$MMX_REL.tmp.$$/.complete"
log "mmx-pwa release staged at $MMX_REL.tmp.$$"

step "migrate"
# Migrate from the STAGED backend tree (identical content to the future
# release dir): migrations must succeed before any live path moves.
# shellcheck source=/dev/null
if [ -f "$LANE_RUNTIME_ENV" ]; then . "$LANE_RUNTIME_ENV"; fi
[ -n "${MULTICA_Z_DB_PASSWORD:-}" ] || fail "no MULTICA_Z_DB_PASSWORD in $LANE_RUNTIME_ENV"
[ -n "${MULTICA_Z_DEV_CODE:-}" ] || fail "no MULTICA_Z_DEV_CODE in $LANE_RUNTIME_ENV"
DSN="${MULTICA_Z_DSN_TEMPLATE/PASSWORD/$MULTICA_Z_DB_PASSWORD}"
( cd "$REL.tmp.$$" && DATABASE_URL="$DSN" "$REL.tmp.$$/bin/migrate" up )
podman exec "$MULTICA_Z_DB_CONTAINER" psql -U hydra -d "$MULTICA_Z_DB_NAME" -tAc \
  'select version from schema_migrations order by version' | sed '/^$/d' | sort > "$LANE_ROOT/data/autodeploy-migrations.post"
comm -13 "$MIG_LIST_PRE" "$LANE_ROOT/data/autodeploy-migrations.post" > "$LANE_ROOT/data/autodeploy-migrations.applied" || true
log "migrations applied: $( [ -s "$LANE_ROOT/data/autodeploy-migrations.applied" ] && paste -sd', ' "$LANE_ROOT/data/autodeploy-migrations.applied" || echo none)"

step "switch"
# Promote staged dirs to live release dirs first, then flip. Before this
# point nothing live has moved: PREV_* markers are recorded here, and a
# failure anywhere above leaves current/previous releases untouched.
# promote_staged never merges into an existing directory (see its contract):
# a same-SHA retry or rollback-then-redeploy replaces the stale tree instead
# of nesting inside it, and a failed promotion restores the prior tree.
promote_staged "$REL" "$REL.tmp.$$" || fail "could not promote backend release $REL"
promote_staged "$MMX_REL" "$MMX_REL.tmp.$$" || fail "could not promote mmx-pwa release $MMX_REL"
# The flip is what makes this a switch, so the rollback marker goes on first.
SWITCHED=1
printf '%s\n' "$PREV_REL" > "$LANE_ROOT/multica-be/previous-release"
printf '%s\n' "$PREV_MMX_REL" > "$LANE_ROOT/mmx-pwa/previous-release"

render_backend_unit "$REL" || fail "could not point $MULTICA_BE_Z_UNIT at releases/${SHA:0:9}"
[ "$(readlink -f "$LANE_ROOT/multica-be/current")" = "$REL" ] || fail "current does not resolve to $REL"
UNIT_REL="$(unit_release)"
[ "$UNIT_REL" = "$LANE_ROOT/multica-be/current" ] \
  || fail "$MULTICA_BE_Z_UNIT runs '$UNIT_REL', not the current symlink"
log "$MULTICA_BE_Z_UNIT -> current -> releases/${SHA:0:9}; restarting"
systemctl --user restart "$MULTICA_BE_Z_UNIT"

step "health"
assert_health "$SHA" || fail "health check failed after the switch"

step "switch-mmx-pwa"
# Flip mmx-pwa/current only after the backend is healthy on the new SHA, so
# a backend failure never leaves the PWA ahead of the API it proxies. The
# env carries only runtime values (REMOTE_API_URL, bypass, bind): the brand
# is baked into the artifact at CI build time, never re-selected here.
ln -sfn "$MMX_REL" "$LANE_ROOT/mmx-pwa/current"
[ "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$MMX_REL" ] || fail "mmx-pwa current does not resolve to $MMX_REL"
write_mmx_pwa_env "$SHA" || fail "could not write mmx-pwa env"
systemctl --user restart "${MMX_PWA_Z_UNIT:?MMX_PWA_Z_UNIT is not set}"
assert_mmx_health "$SHA" || fail "mmx-pwa health check failed after the switch"
printf 'updated (mmx-pwa-z restarted to %s)\n' "${SHA:0:9}" > "$LANE_ROOT/data/autodeploy-mmx-pwa.note"
log "mmx-pwa z -> releases/${SHA:0:9}; restarted and healthy"

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
  else
    log "plugin bundle changed ($OLD_HASH -> $NEW_HASH); installing and restarting $DSH_Z_WEB_UNIT"
    rsync -a --delete "$ART/dsh-plugin/" "$DSH_Z_PLUGIN_BUNDLE/"
    printf 'updated (dsh-z restarted)\n' > "$LANE_ROOT/data/autodeploy-plugin.note"
    systemctl --user restart "$DSH_Z_WEB_UNIT"
    wait_http "$(resolve_lane_web_url)/" 90 '' || fail "lane web did not come back after the plugin update"
    assert_health "$SHA" || fail "health check failed after the plugin update"
  fi
else
  printf 'unchanged (no dsh-plugin artifact in this run)\n' > "$LANE_ROOT/data/autodeploy-plugin.note"
  log "no dsh-plugin artifact in run $RUN_ID; keeping the installed bundle"
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
