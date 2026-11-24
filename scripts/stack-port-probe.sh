#!/usr/bin/env bash
# scripts/stack-port-probe.sh — M-556 option C: detect a dead rootless port
# forwarder on the production stack and heal it.
#
# Rootless podman publishes container ports through per-port `rootlessport`
# processes. When one dies (or goes stale after an unclean container exit),
# the container keeps reporting healthy while the host port is dead. A
# hung backend is the other failure mode: the process freezes, podman marks
# the container Unhealthy, and `restart: unless-stopped` never fires because
# the process never exits. This script heals both, for the backend and the
# web frontend:
#
#   container ok + host dead   = forwarder dead -> kill stale forwarder pids,
#                                restart that container only
#   container dead ×3 ticks    = hung backend -> capture diagnostics (top,
#                                /proc status, logs, SIGQUIT goroutine dump
#                                for the backend), then restart it
#   both ok                    = healthy, nothing to do
#
# Restarts are rate-limited (2 per 15 minutes per container) and never happen
# while deploy-main.sh holds its lock. Only the backend/frontend containers
# are ever restarted — never the database, never a compose-wide operation.
#
# Usage: stack-port-probe.sh [--check] [stack-dir]
#   --check   report only: exit 1 when a forwarder is dead, never restart.
#   stack-dir defaults to $HOME/.local/share/multica.
set -euo pipefail

CHECK_ONLY=0
if [ "${1:-}" = "--check" ]; then CHECK_ONLY=1; shift; fi
STACK_DIR="${1:-$HOME/.local/share/multica}"
LOCK_FILE="${MULTICA_MAIN_LOCK_FILE:-/mnt/workspace/multica-main-deploy.lock}"
STATE_DIR="${PORT_PROBE_STATE_DIR:-/mnt/workspace/multica-main-port-probe}"
# Rate limit: at most this many restarts per window per container.
MAX_RESTARTS=2
WINDOW_SECONDS=900

log() { printf '[port-probe %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

[ -d "$STACK_DIR" ] || die "stack directory not found: $STACK_DIR"
[ -f "$STACK_DIR/.env" ] || die "stack environment file not found: $STACK_DIR/.env"
[ -f "$STACK_DIR/compose.yml" ] || die "compose file not found: $STACK_DIR/compose.yml"
for cmd in awk curl jq podman; do
  command -v "$cmd" >/dev/null 2>&1 || die "required command not found: $cmd"
done

read_env() { # $1 = key, $2 = default; simple KEY=value from the stack env
  local value
  value="$(awk -F= -v key="$1" '
    $1 == key {
      sub(/^[^=]*=/, "")
      if ((substr($0, 1, 1) == "\047" && substr($0, length($0), 1) == "\047") ||
          (substr($0, 1, 1) == "\042" && substr($0, length($0), 1) == "\042")) {
        $0 = substr($0, 2, length($0) - 2)
      }
      print
      found++
    }
    END {if (found > 1) exit 2}' "$STACK_DIR/.env" 2>/dev/null || true)"
  [ -n "$value" ] || value="$2"
  printf '%s' "$value"
}

container_id() { # $1 = compose service; resolves via compose labels
  # The host has no built-in `podman compose` (it delegates to the external
  # podman-compose 1.5.0, which has no `ps -q <service>`), so resolve through
  # plain `podman ps` label filters. The project is the stack directory name,
  # the same convention compose itself uses (multica, probe-drill, ...).
  local project
  project="$(basename "$STACK_DIR")"
  podman ps -q \
    --filter "label=com.docker.compose.project=$project" \
    --filter "label=com.docker.compose.service=$1" 2>/dev/null | head -1
}

host_ok() { # $1 = url; the host side of a published port
  curl -fsS --max-time 5 -o /dev/null "$1" 2>/dev/null
}

container_ok() { # $1 = container id, $2 = in-container url
  # -T 5 --tries=1: a frozen process still holds the socket open while never
  # answering, so an untimed wget would hang until its 15-minute default.
  podman exec "$1" wget -q -T 5 --tries=1 -O /dev/null "$2" 2>/dev/null
}

restart_allowed() { # $1 = service; true when under the rate limit
  local now cutoff count f
  now="$(date +%s)"
  cutoff=$((now - WINDOW_SECONDS))
  count=0
  mkdir -p "$STATE_DIR"
  for f in "$STATE_DIR/$1".restart.*; do
    [ -e "$f" ] || continue
    ts="${f##*.restart.}"
    [[ "$ts" =~ ^[0-9]+$ ]] || continue
    if [ "$ts" -lt "$cutoff" ]; then
      rm -f "$f"
    else
      count=$((count + 1))
    fi
  done
  [ "$count" -lt "$MAX_RESTARTS" ]
}

record_restart() { # $1 = service
  touch "$STATE_DIR/$1.restart.$(date +%s)"
}

# Consecutive-tick counter for the hung-container path: a single failed
# in-container check can be a blip, so the container is only restarted after
# UNHEALTHY_TICKS failures in a row (a healthy tick resets the count).
UNHEALTHY_TICKS=3

unhealthy_count() { # $1 = service; prints the consecutive count, 0 if none
  local f="$STATE_DIR/$1.unhealthy" count
  [ -f "$f" ] || { printf '0'; return; }
  count="$(cat "$f" 2>/dev/null || printf '0')"
  [[ "$count" =~ ^[0-9]+$ ]] || count=0
  printf '%s' "$count"
}

bump_unhealthy() { # $1 = service; prints the new consecutive count
  local count
  count=$(( $(unhealthy_count "$1") + 1 ))
  printf '%s' "$count" > "$STATE_DIR/$1.unhealthy"
  printf '%s' "$count"
}

clear_unhealthy() { # $1 = service
  rm -f "$STATE_DIR/$1.unhealthy"
}

container_pid() { # $1 = container id; host pid of the container init, or nothing
  podman inspect --format '{{.State.Pid}}' "$1" 2>/dev/null || true
}

capture_hang() { # $1 = service, $2 = container id; diagnostics BEFORE restart
  local pid
  pid="$(container_pid "$2")"
  [ -n "$pid" ] && [ "$pid" != 0 ] || { log "$1: no container pid; skipping hang capture"; return 0; }
  log "$1: --- podman top:"; podman top "$2" 2>/dev/null | head -20 || log "(podman top failed)"
  log "$1: --- /proc/$pid status+wchan:"
  if [ -r "/proc/$pid/status" ]; then
    grep -E '^(State|Threads|VmRSS)' "/proc/$pid/status" 2>/dev/null || true
    printf 'wchan: %s\n' "$(cat "/proc/$pid/wchan" 2>/dev/null || printf unknown)"
  else
    log "(cannot read /proc/$pid)"
  fi
  log "$1: --- container logs (tail 100):"; podman logs --tail 100 "$2" 2>&1 | tail -100
  if [ "$1" = backend ]; then
    # SIGQUIT makes the Go runtime write a goroutine dump to stderr (and
    # kills the process, which is about to be restarted anyway). The dump
    # lands at the end of the container log; tail it into the evidence.
    log "$1: sending SIGQUIT for the goroutine dump"
    podman kill -s QUIT "$2" 2>/dev/null || log "$1: SIGQUIT failed (already dead?)"
    sleep 5
    log "$1: --- container logs after QUIT (tail 100):"; podman logs --tail 100 "$2" 2>&1 | tail -100
  fi
}

stale_forwarder_pids() { # $1 = host port; pids of rootlessport listeners on it
  ss -ltnp 2>/dev/null | grep -E ":$1[[:space:]]" \
    | sed -n 's/.*rootlessport",pid=\([0-9][0-9]*\).*/\1/p' || true
}

restart_container() { # $1 = service, $2 = container id, $3 = host port
  # A stale forwarder can hold the host port even though it forwards nothing;
  # a plain restart then fails with "bind: address already in use" and the
  # container stays down. Kill the exact pids bound to the port first —
  # never by name alone.
  local svc="$1" cid="$2" hport="$3" pid
  for pid in $(stale_forwarder_pids "$hport"); do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    log "$svc: killing stale forwarder pid $pid on :$hport"
    env kill -9 "$pid" 2>/dev/null || log "$svc: pid $pid already gone"
  done
  podman restart "$cid" >/dev/null || { log "$svc: podman restart failed"; return 1; }
}

capture_evidence() { # $1 = service, $2 = host port; log both sides' state
  # Best effort: ss/ps may be absent on minimal runners; the classification
  # above is the evidence that matters.
  log "evidence for $1 (host port $2):"
  if command -v ss >/dev/null 2>&1; then
    log "--- ss listeners:"; ss -ltnp 2>/dev/null | grep -E ":$2\\b" || log "(nothing listening on $2)"
  else
    log "--- ss unavailable"
  fi
  if command -v ps >/dev/null 2>&1; then
    log "--- rootlessport:"; ps -o pid,ppid,etime,cmd -C rootlessport 2>/dev/null || log "(no rootlessport processes)"
  else
    log "--- ps unavailable"
  fi
  log "--- podman port:"; podman port "$(container_id "$1")" 2>/dev/null || log "(podman port failed)"
}

probe_service() { # $1 = service, $2 = host port, $3 = host path, $4 = container port, $5 = container path
  local svc="$1" hport="$2" hpath="$3" cport="$4" cpath="$5" cid
  cid="$(container_id "$svc")"
  [ -n "$cid" ] || { log "$svc: no container found; reporting (not restarting unknown state)"; return 1; }
  if container_ok "$cid" "http://127.0.0.1:$cport$cpath"; then
    clear_unhealthy "$svc"
    if host_ok "http://127.0.0.1:$hport$hpath"; then
      log "$svc: healthy (container ok, host :$hport ok)"
      return 0
    fi
    log "$svc: FORWARDER DEAD (container ok, host :$hport dead)"
    capture_evidence "$svc" "$hport"
    printf '%s %s forwarder-dead host-port=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$svc" "$hport" >> "$STATE_DIR/probe.log"
    if [ "$CHECK_ONLY" = 1 ]; then return 1; fi
    if ! restart_allowed "$svc"; then
      log "$svc: rate limit reached ($MAX_RESTARTS restarts in $((WINDOW_SECONDS / 60)) min); NOT restarting"
      return 1
    fi
    log "$svc: restarting container $cid (forwarder healing)"
    restart_container "$svc" "$cid" "$hport" || return 1
    record_restart "$svc"
    sleep 10
    if host_ok "http://127.0.0.1:$hport$hpath"; then
      log "$svc: healed (host :$hport answers after restart)"
      return 0
    fi
    log "$svc: still dead after restart; NOT retrying in this tick"
    return 1
  fi
  count="$(bump_unhealthy "$svc")"
  log "$svc: CONTAINER UNHEALTHY (in-container check failed, $count/$UNHEALTHY_TICKS consecutive)"
  [ "$count" -ge "$UNHEALTHY_TICKS" ] || return 1
  log "$svc: unhealthy for $UNHEALTHY_TICKS consecutive ticks; preparing restart"
  capture_hang "$svc" "$cid"
  printf '%s %s container-unhealthy host-port=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$svc" "$hport" >> "$STATE_DIR/probe.log"
  if [ "$CHECK_ONLY" = 1 ]; then return 1; fi
  if ! restart_allowed "$svc"; then
    log "$svc: rate limit reached ($MAX_RESTARTS restarts in $((WINDOW_SECONDS / 60)) min); NOT restarting"
    return 1
  fi
  log "$svc: restarting hung container $cid"
  restart_container "$svc" "$cid" "$hport" || return 1
  record_restart "$svc"
  clear_unhealthy "$svc"
  sleep 10
  if container_ok "$cid" "http://127.0.0.1:$cport$cpath"; then
    log "$svc: revived (in-container check answers after restart)"
    return 0
  fi
  log "$svc: still unhealthy after restart; NOT retrying in this tick"
  return 1
}

# A deploy owns the stack while it holds this lock. The timer must never
# restart a container mid-deploy; it skips the tick instead. deploy-main.sh
# invokes the probe with PORT_PROBE_SKIP_LOCK=1 because it holds the lock
# itself and wants the verdict, not the skip.
if [ "${PORT_PROBE_SKIP_LOCK:-0}" != 1 ]; then
  exec 200>"$LOCK_FILE" 2>/dev/null || die "cannot open lock file $LOCK_FILE"
  if ! flock -n 200 2>/dev/null; then
    log "deploy lock held; skipping this tick"
    exit 0
  fi
fi

BACKEND_PORT="$(read_env BACKEND_PORT 3702)"
FRONTEND_PORT="$(read_env FRONTEND_INTERNAL_PORT 3703)"
[[ "$BACKEND_PORT" =~ ^[0-9]{1,5}$ ]] || die "invalid BACKEND_PORT: $BACKEND_PORT"
[[ "$FRONTEND_PORT" =~ ^[0-9]{1,5}$ ]] || die "invalid FRONTEND_INTERNAL_PORT: $FRONTEND_PORT"
mkdir -p "$STATE_DIR"

rc=0
probe_service backend "$BACKEND_PORT" /health 8080 /healthz || rc=1
probe_service frontend "$FRONTEND_PORT" /login 3000 /login || rc=1
if [ "$rc" = 0 ]; then
  log "all sides healthy"
else
  log "probe finished with findings (see above)"
fi
exit "$rc"
