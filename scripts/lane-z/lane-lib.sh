#!/usr/bin/env bash
# scripts/lane-z/lane-lib.sh — helpers shared by autodeploy.sh and rollback.sh.
#
# Sourced, not executed. The caller must have sourced dsh-z.env (it uses
# $LANE_ROOT, $MULTICA_BE_Z_UNIT) and must define log().

# The backend unit must follow `multica-be/current`, not a release directory.
# deploy/dsh-z/deploy.sh pins one release path per install, and systemd never
# re-resolves a symlink for a running unit: a switch that only flips `current`
# then restarts the OLD binary, so the lane serves the previous commit while
# `current/release.json` advertises the new one. Pointing WorkingDirectory and
# ExecStart at `current` makes every restart run whatever the symlink names, so
# a switch is just "flip the symlink and restart".
render_backend_unit() { # $1 = release `current` must point at; flips the symlink
  local rel="$1" tmpl out tmp cur="$LANE_ROOT/multica-be/current"
  [ -n "$rel" ] && [ -d "$rel" ] || { log "render_backend_unit: '$rel' is not a release directory"; return 1; }
  ln -sfn "$rel" "$cur"
  tmpl="$SCRIPT_DIR/units/multica-be-z.service"
  [ -f "$tmpl" ] || tmpl="$SCRIPT_DIR/../../deploy/dsh-z/units/multica-be-z.service"
  [ -f "$tmpl" ] || { log "render_backend_unit: no multica-be-z.service template found"; return 1; }
  out="$HOME/.config/systemd/user/$MULTICA_BE_Z_UNIT"
  tmp="$out.tmp.$$"
  mkdir -p "$(dirname "$out")"
  sed -e "s|__RELEASE_DIR__|$cur|g" \
      -e "s|__SERVER_ENV__|$LANE_ROOT/multica-be/env|g" "$tmpl" > "$tmp"
  if grep -q '__[A-Z_]*__' "$tmp"; then
    log "render_backend_unit: unsubstituted placeholder in the rendered unit"
    rm -f "$tmp"
    return 1
  fi
  chmod 600 "$tmp"
  mv "$tmp" "$out"
  systemctl --user daemon-reload
}

unit_release() { # release path the installed backend unit runs, or nothing
  systemctl --user cat "$MULTICA_BE_Z_UNIT" 2>/dev/null \
    | sed -n 's|^ExecStart=\(.*\)/bin/server$|\1|p' | head -1
}

prune_releases() {
  prune_release_tree "$LANE_ROOT/multica-be/releases" "$LANE_ROOT/multica-be/current" "$AUTODEPLOY_KEEP_RELEASES" "pruned-backend"
  prune_release_tree "$LANE_ROOT/mmx-pwa/releases" "$LANE_ROOT/mmx-pwa/current" "$AUTODEPLOY_KEEP_RELEASES" "pruned-mmx-pwa"
}

prune_release_tree() { # $1 = releases dir, $2 = current symlink, $3 = keep, $4 = log tag
  local releases_dir="$1" current_link="$2" keep="$3" tag="$4"
  local current daemon_pid daemon_exe daemon_release proc_root
  current="$(readlink -f "$current_link" 2>/dev/null || true)"
  daemon_release=""
  proc_root="${MULTICA_LANE_PROC_ROOT:-/proc}"
  daemon_pid="$(systemctl --user show --property=MainPID --value "$MULTICA_DAEMON_Z_UNIT" 2>/dev/null || true)"
  if [[ "$daemon_pid" =~ ^[0-9]+$ ]] && [ "$daemon_pid" -gt 1 ] && [ -r "$proc_root/$daemon_pid/exe" ]; then
    daemon_exe="$(readlink "$proc_root/$daemon_pid/exe" 2>/dev/null || true)"
    daemon_exe="${daemon_exe% (deleted)}"
    case "$daemon_exe" in
      "$LANE_ROOT"/multica-be/releases/*/bin/multica)
        daemon_release="${daemon_exe%/bin/multica}"
        ;;
    esac
  fi

  [ -d "$releases_dir" ] || return 0
  # shellcheck disable=SC2012
  ls -1dt "$releases_dir"/*/ 2>/dev/null | tail -n "+$((keep + 1))" | while read -r dir; do
    [ "$(readlink -f "$dir")" = "$current" ] && continue
    [ -n "$daemon_release" ] && [ "$(readlink -f "$dir")" = "$daemon_release" ] && continue
    log "$tag: pruning old release $(basename "$dir")"
    rm -rf "$dir"
  done
}

# A release built by CI and one built by deploy/dsh-z/deploy.sh stamp the same
# commit differently: the Makefile uses `git rev-parse --short HEAD` (7 chars),
# deploy.sh uses the full 40. Both identify the same build, so a health check
# accepts either — but never an empty or too-short value, which would match
# anything.
commit_matches() { # $1 = commit the lane reports, $2 = sha it must be
  local got="$1" want="$2"
  [ -n "$got" ] && [ -n "$want" ] || return 1
  [ "$got" = "$want" ] && return 0
  [ "${#got}" -ge 7 ] && [ "${want#"$got"}" != "$want" ]
}

lane_multica_cli() { # prints the lane CLI path, if present
  printf '%s' "$LANE_ROOT/multica-be/current/bin/multica"
}

# daemon_health_json prints the lane daemon's /health JSON for profile z, or an
# empty object on failure. Uses the lane CLI (which resolves the profile's
# health port) so autodeploy does not guess the port.
daemon_health_json() {
  local cli out
  cli="$(lane_multica_cli)"
  [ -x "$cli" ] || { printf '{}'; return 0; }
  out="$(env -u MULTICA_SERVER_URL -u MULTICA_TOKEN -u MULTICA_WORKSPACE_ID \
    -u MULTICA_DAEMON_PORT -u MULTICA_DAEMON_HEALTH_PORT -u MULTICA_PROFILE \
    HOME="$LANE_HOME" "$cli" --profile "$MULTICA_Z_PROFILE" daemon status --output json 2>/dev/null || true)"
  if [ -n "$out" ]; then
    printf '%s' "$out"
  else
    printf '{}'
  fi
}

daemon_active_lifecycle_count() { # $1 = health json
  local health="${1-}"
  [ -n "$health" ] || health="{}"
  printf '%s' "$health" | jq -r '(.active_task_count // 0) | tonumber' 2>/dev/null || printf '0'
}

daemon_is_idle() { # $1 = health json
  local active
  active="$(daemon_active_lifecycle_count "$1")"
  [ "$active" = "0" ]
}

daemon_runs_release() { # $1 = health json, $2 = target sha
  local health="${1-}" commit
  [ -n "$health" ] || health="{}"
  commit="$(printf '%s' "$health" | jq -r '.cli_commit // empty' 2>/dev/null || true)"
  commit_matches "$commit" "$2"
}

daemon_refresh_pending_file() {
  printf '%s/data/autodeploy-daemon-refresh.pending\n' "$LANE_ROOT"
}

clear_daemon_refresh_pending() {
  rm -f "$(daemon_refresh_pending_file)"
}

mark_daemon_refresh_pending() { # $1 = target sha
  local f tmp
  f="$(daemon_refresh_pending_file)"
  tmp="$f.tmp.$$"
  printf '%s\n' "$1" > "$tmp"
  mv -f "$tmp" "$f"
}

wait_daemon_release() { # $1 = target sha, $2 = timeout seconds
  local deadline=$((SECONDS + ${2:-120})) health
  while [ "$SECONDS" -lt "$deadline" ]; do
    health="$(daemon_health_json)"
    if [ "$(printf '%s' "$health" | jq -r '.status // empty' 2>/dev/null || true)" = "running" ] \
      && daemon_runs_release "$health" "$1"; then
      return 0
    fi
    sleep 2
  done
  return 1
}

# refresh_lane_daemon_after_deploy refreshes multica-daemon-z.service so a
# successful backend switch activates the deployed session logic. It never
# interrupts an active task: if the daemon is busy it defers (records a pending
# marker for the next tick) and lets the daemon's own auto-reload restart when
# idle. M-232's pruned-binary recovery is separate and not duplicated here.
refresh_lane_daemon_after_deploy() { # $1 = target sha
  local sha="${1:?sha required}" unit health active
  unit="${MULTICA_DAEMON_Z_UNIT:-multica-daemon-z.service}"
  health="$(daemon_health_json)"

  if daemon_runs_release "$health" "$sha"; then
    clear_daemon_refresh_pending
    log "daemon z already running ${sha:0:9}"
    return 0
  fi

  if ! daemon_is_idle "$health"; then
    active="$(daemon_active_lifecycle_count "$health")"
    mark_daemon_refresh_pending "$sha"
    log "daemon z has $active active task(s); deferring refresh to ${sha:0:9} (no interruption)"
    return 0
  fi

  log "refreshing $unit to run ${sha:0:9}"
  systemctl --user restart "$unit"
  if wait_daemon_release "$sha" 120; then
    clear_daemon_refresh_pending
    log "daemon z refreshed: running ${sha:0:9}"
    return 0
  fi

  mark_daemon_refresh_pending "$sha"
  log "daemon z restart did not reach ${sha:0:9} in time; will retry"
  return 1
}

wait_http() { # $1=url $2=timeout $3=expected substring
  local deadline=$((SECONDS + $2)) body
  while [ "$SECONDS" -lt "$deadline" ]; do
    body="$(curl -fsS --max-time 3 "$1" 2>/dev/null || true)"
    if [ -n "$body" ] && { [ -z "${3:-}" ] || printf '%s' "$body" | grep -q "$3"; }; then
      return 0
    fi
    sleep 2
  done
  return 1
}
