#!/usr/bin/env bash
# Deploy the main Multica stack from an independent host runner.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: deploy-main.sh <image-tag> <expected-commit-sha> <max-drain-seconds> [--force] [--dry-run]

max-drain-seconds 0 (the CI/CD default) does not wait for active tasks: it
cancels them by stopping the daemon and deploys immediately. A positive
value waits that long for tasks to drain; --force stops the daemon and
cancels remaining tasks at the timeout instead of failing.
--dry-run prints the plan.
EOF
}

log() { printf '[deploy-main %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

# One deploy at a time (M-556 option C): a second deploy sees the lock held
# and aborts instead of racing the switch. This block runs BEFORE argument
# parsing so "$@" is still whole for the re-exec below.
#
# The lock must never be held on an open fd inside this shell: every child
# (podman, compose, conmon) inherits open fds, and conmon outlives the
# deploy, so the lock would never free and every later tick/deploy would
# refuse. Instead flock itself holds the lock and closes the fd on exec
# (-o), so no descendant can ever inherit it.
WORKSPACE_ROOT="${MULTICA_MAIN_WORKSPACE_ROOT:-/mnt/workspace}"
LOCK_FILE="${MULTICA_MAIN_LOCK_FILE:-$WORKSPACE_ROOT/multica-main-deploy.lock}"
# Local drain control is authenticated by a 0600 credential file published by
# the daemon itself. The endpoint is bound to 127.0.0.1; the token stops any
# other local process from pausing claim paths.
DRAIN_CONTROL_FILE="${MULTICA_DAEMON_DRAIN_FILE:-$HOME/.multica/daemon-drain.json}"
STOP_AUDIT_LOG="${MULTICA_DEPLOY_STOP_AUDIT_LOG:-$WORKSPACE_ROOT/multica-main-stop-audit.log}"
DRAIN_UNIT="multica-daemon.service"
DEPLOY_ACTOR="${GITHUB_ACTOR:-${USER:-unknown}}"
DEPLOY_RUN_ID="${GITHUB_RUN_ID:-}"

# M-556: the GH runner reaps every process carrying its RUNNER_TRACKING_ID
# when the job ends — including conmon, which outlives the deploy and takes
# the stack's ports with it. Clear it so nothing this script spawns can ever
# match a job id.
export RUNNER_TRACKING_ID=
case "$LOCK_FILE" in "$WORKSPACE_ROOT"/*) ;; *) die "lock file must be under $WORKSPACE_ROOT" ;; esac
if [ -z "${DEPLOY_MAIN_LOCKED:-}" ]; then
  export DEPLOY_MAIN_LOCKED=1
  # set +e: a conflicting lock makes flock exit non-zero, and the code must
  # read it instead of letting errexit fire first.
  set +e
  flock -n -o -E 75 "$LOCK_FILE" bash "$0" "$@"
  rc=$?
  set -e
  [ "$rc" -eq 75 ] && die "another deploy holds the lock ($LOCK_FILE); refusing to run concurrently"
  exit "$rc"
fi

if [ "$#" -lt 3 ]; then usage >&2; exit 2; fi
IMAGE_TAG="$1"
EXPECTED_COMMIT="$2"
MAX_DRAIN_SECONDS="$3"
shift 3
FORCE=0
DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $arg" ;;
  esac
done

[[ "$IMAGE_TAG" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || die "invalid image tag"
[[ "$EXPECTED_COMMIT" =~ ^[0-9a-fA-F]{40}$ ]] || die "expected commit must be a full 40-character SHA"
[[ "$MAX_DRAIN_SECONDS" =~ ^[0-9]+$ ]] || die "max drain wait must be a non-negative number of seconds (0 = cancel tasks and deploy immediately)"

# Image tags embed the built short SHA by convention (main-<version>-<sha>).
# A tag whose trailing dash-segment is 7-40 hex chars must match the leading
# characters of the expected commit, so a stale tag built from an older commit
# fails fast here instead of shipping mismatched version labels (M-539:
# tag main-0.11.1-7239fad88 shipped commit 4bdc0890c).
if [[ "$IMAGE_TAG" =~ -([0-9a-fA-F]{7,40})$ ]]; then
  tag_sha="${BASH_REMATCH[1],,}"
  commit_prefix="${EXPECTED_COMMIT:0:${#tag_sha}}"
  commit_prefix="${commit_prefix,,}"
  [ "$tag_sha" = "$commit_prefix" ] || die "image tag '$IMAGE_TAG' embeds short SHA '${BASH_REMATCH[1]}', which is not a prefix of expected commit $EXPECTED_COMMIT"
fi

STACK_DIR="${MULTICA_MAIN_STACK_DIR:-$HOME/.local/share/multica}"
ENV_FILE="$STACK_DIR/.env"
CONTAINERS_ROOT="${MULTICA_MAIN_CONTAINERS_ROOT:-/mnt/containers}"
BACKUP_ROOT="${MULTICA_MAIN_BACKUP_ROOT:-$WORKSPACE_ROOT/multica-main-backups}"
PROC_ROOT="${MULTICA_MAIN_PROC_ROOT:-/proc}"
BACKEND_PORT=""
FRONTEND_PORT=""
ENV_TMP=""
MIN_FREE_KB=$((10 * 1024 * 1024))
BACKEND_IMAGE=""
WEB_IMAGE=""
OLD_TAG=""
BACKUP_DIR=""
SWITCH_STARTED=0
ROLLBACK_ACTIVE=0
DAEMON_WAS_STOPPED=0
DRAIN_TOKEN=""
DRAIN_PORT=""
DRAIN_LEASE_TOKEN=""
DRAIN_LEASE_TIMEOUT=0

for cmd in awk cat chmod cp date df find flock grep install jq mkdir mktemp multica mv podman ps rm seq sleep systemctl; do
  command -v "$cmd" >/dev/null 2>&1 || die "required command not found: $cmd"
done
command -v curl >/dev/null 2>&1 || die "required command not found: curl"

# (Re)create the whole stack through the persistent multica.service unit, NOT
# through this shell and NOT through a transient systemd-run unit (verified
# 2026-11-25: a job-spawned compose leaves conmon carrying the job id, which
# the runner reaps when the job ends; transient units take conmon down with
# them on this host). The unit's manager environment never carries a job id.
stack_up() {
  systemctl --user restart multica.service
}

# The deploy runner must be a sibling of the daemon, never one of its tasks.
# Check both process arguments and cgroups so systemd service ancestry is caught
# even when the daemon's executable has a generic process name.
assert_independent_runner() {
  local pid="$PPID" row ppid args cgroup depth=0
  while [ "$pid" -gt 1 ] && [ "$depth" -lt 64 ]; do
    row="$(ps -o ppid=,args= -p "$pid" 2>/dev/null || true)"
    read -r ppid args <<< "$row"
    if [[ "$args" =~ (^|[/[:space:]])multica-daemon([.]service)?([[:space:]]|$) ]] || \
       [[ "$args" =~ multica[[:space:]]+daemon[[:space:]]+(start|run) ]]; then
      die "refusing to deploy from a process descended from multica-daemon (pid $pid)"
    fi
    if [ -r "$PROC_ROOT/$pid/cgroup" ]; then
      cgroup="$(cat "$PROC_ROOT/$pid/cgroup")"
      if [[ "$cgroup" == *multica-daemon.service* ]]; then
        die "refusing to deploy from multica-daemon.service cgroup (pid $pid)"
      fi
    fi
    [[ "$ppid" =~ ^[0-9]+$ ]] || break
    pid="$ppid"
    depth=$((depth + 1))
  done
}
assert_independent_runner

# A single daemon-status read can fail while the daemon restarts (M-556: "no
# numeric active_task_count" aborted a release before any change). Retry
# briefly instead of dying on one bad read.
daemon_active_count() { # prints the count, or nothing on failure
  local tries=0 daemon_json count
  while [ "$tries" -lt 5 ]; do
    if daemon_json="$(multica daemon status --output json 2>/dev/null)"; then
      count="$(jq -r '.active_task_count // empty' <<<"$daemon_json" 2>/dev/null || true)"
      if [[ "$count" =~ ^[0-9]+$ ]]; then printf '%s' "$count"; return 0; fi
    fi
    tries=$((tries + 1))
    sleep 3
  done
  return 1
}

# Every daemon stop is preceded by one structured UTC audit record. It goes to
# stdout (captured by the workflow) and to a workspace-local JSONL file so a
# later investigation can attribute stops that the journal cannot.
audit_stop() { # operation outcome timeout_seconds force_bool
  local operation="$1" outcome="$2" timeout="$3" force="$4"
  local line
  line="$(jq -nc \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg operation "$operation" --arg actor "$DEPLOY_ACTOR" \
    --arg workflow_run_id "$DEPLOY_RUN_ID" --arg unit "$DRAIN_UNIT" \
    --argjson force "$force" --argjson timeout "$timeout" \
    --arg outcome "$outcome" \
    '{ts:$ts,operation:$operation,actor:$actor,workflow_run_id:$workflow_run_id,unit:$unit,force:$force,timeout:$timeout,outcome:$outcome}')" || return 0
  printf '%s\n' "$line"
  mkdir -p "$(dirname "$STOP_AUDIT_LOG")" 2>/dev/null || true
  printf '%s\n' "$line" >> "$STOP_AUDIT_LOG" 2>/dev/null || true
}

# Read the daemon-published control credential and actual health port.
drain_control() { # prints token, then port; fails if either is missing
  [ -r "$DRAIN_CONTROL_FILE" ] || return 1
  local control token port
  control="$(cat "$DRAIN_CONTROL_FILE")" || return 1
  token="$(jq -r '.token // empty' <<<"$control")" || return 1
  port="$(jq -r '.port // empty' <<<"$control")" || return 1
  [ -n "$token" ] && [[ "$port" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n%s' "$token" "$port"
}

drain_curl() { # method path [header] [json body]
  local method="$1" path="$2" header="${3:-}" body="${4:-}"
  local args=(-fsS --max-time 5 -X "$method" -H "Authorization: Bearer $DRAIN_TOKEN")
  [ -n "$header" ] && args+=(-H "$header")
  if [ -n "$body" ]; then
    args+=(-H 'Content-Type: application/json' --data "$body")
  fi
  curl "${args[@]}" "http://127.0.0.1:$DRAIN_PORT$path"
}

# Take a uniquely owned, expiring lease. The TTL includes a small grace over
# the script's own deadline so the daemon never expires the lease while the
# script is still legitimately waiting for idle.
request_drain() { # prints the lease token
  local control body json lease ttl
  control="$(drain_control)" || return 1
  DRAIN_TOKEN="${control%%$'\n'*}"
  DRAIN_PORT="${control##*$'\n'}"
  ttl=$((MAX_DRAIN_SECONDS + 10))
  body="$(jq -nc \
    --arg actor "$DEPLOY_ACTOR" --arg run "$DEPLOY_RUN_ID" --arg unit "$DRAIN_UNIT" \
    --argjson force "$([ "$FORCE" = 1 ] && printf true || printf false)" \
    --argjson timeout "$ttl" \
    '{actor:$actor,workflow_run_id:$run,unit:$unit,force:$force,timeout_seconds:$timeout}')" || return 1
  json="$(drain_curl POST /drain/begin "" "$body")" || return 1
  lease="$(jq -r '.token // empty' <<<"$json")" || return 1
  [ -n "$lease" ] || return 1
  DRAIN_LEASE_TOKEN="$lease"
  DRAIN_LEASE_TIMEOUT="$ttl"
  printf '%s' "$lease"
}

# Best-effort release: if the script has lost control of the lease, aborting
# still resumes claims on the daemon side before we give up or force-stop.
drain_abort_best_effort() {
  [ -n "$DRAIN_LEASE_TOKEN" ] || return 0
  drain_curl POST /drain/abort "X-Multica-Drain-Token: $DRAIN_LEASE_TOKEN" >/dev/null 2>&1 || true
  audit_stop drain_abort resumed "$DRAIN_LEASE_TIMEOUT" "$([ "$FORCE" = 1 ] && printf true || printf false)" || true
}

# 0 = idle, 1 = deadline passed, 2 = lease lost/expired.
wait_for_drain() { # $1 = lease token
  local lease="$1" deadline=$((SECONDS + MAX_DRAIN_SECONDS)) state active claims json
  while [ "$SECONDS" -lt "$deadline" ]; do
    json="$(drain_curl GET /drain/status "X-Multica-Drain-Token: $lease")" || return 2
    state="$(jq -r '.status // empty' <<<"$json")" || return 2
    active="$(jq -r '.active_task_count // empty' <<<"$json")" || return 2
    claims="$(jq -r '.claims_in_flight // empty' <<<"$json")" || return 2
    active_count="$active"
    case "$state" in
      drained)
        [ "$active" = 0 ] && [ "$claims" = 0 ] && return 0
        ;;
      draining) ;;
      *) return 2 ;;
    esac
    sleep 2
  done
  return 1
}

read_env_value() { # $1 = key; prints only a simple KEY=value from the stack env
  awk -F= -v key="$1" '
    $1 == key {
      sub(/^[^=]*=/, "")
      if ((substr($0, 1, 1) == "\047" && substr($0, length($0), 1) == "\047") ||
          (substr($0, 1, 1) == "\042" && substr($0, length($0), 1) == "\042")) {
        $0 = substr($0, 2, length($0) - 2)
      }
      print
      found++
    }
    END {if (found > 1) exit 2}
  ' "$ENV_FILE"
}
free_kb() { df -Pk "$1" | awk 'NR==2 {print $4}'; }

# Post-run housekeeping: empty the deploy runner's TMPDIR and report free
# space per mount. Runs after success and after failure (from the rollback
# trap). Refuses to empty TMPDIR when it is the workspace root itself or the
# shared tmp root, which hold caches and scratch belonging to other processes.
post_run() {
  case "${TMPDIR:-}" in
    "$WORKSPACE_ROOT" | "$WORKSPACE_ROOT"/tmp) ;;
    *)
      if [ -n "${TMPDIR:-}" ] && [ -d "$TMPDIR" ]; then
        find "$TMPDIR" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + 2>/dev/null || true
        log "emptied runner TMPDIR: $TMPDIR"
      fi
      ;;
  esac
  local path available
  for path in "$CONTAINERS_ROOT" "$WORKSPACE_ROOT"; do
    available="$(free_kb "$path" 2>/dev/null || true)"
    if [[ "$available" =~ ^[0-9]+$ ]]; then
      log "mount $path has $(awk -v kb="$available" 'BEGIN {printf "%.1f", kb / 1048576}') GiB free"
    else
      log "could not read free space for $path"
    fi
  done
}

[ -d "$STACK_DIR" ] || die "stack directory not found: $STACK_DIR"
[ -f "$ENV_FILE" ] || die "stack environment file not found: $ENV_FILE"
[ -x "$HOME/.local/bin/multica" ] || die "old daemon binary is missing at $HOME/.local/bin/multica"
grep -q '^MULTICA_IMAGE_TAG=' "$ENV_FILE" || die "MULTICA_IMAGE_TAG is missing from $ENV_FILE"
OLD_TAG="$(read_env_value MULTICA_IMAGE_TAG)" || die "could not read current image tag"
[ -n "$OLD_TAG" ] || die "current MULTICA_IMAGE_TAG is empty"
BACKEND_REPO="$(read_env_value MULTICA_BACKEND_IMAGE || true)"
WEB_REPO="$(read_env_value MULTICA_WEB_IMAGE || true)"
BACKEND_REPO="${BACKEND_REPO:-ghcr.io/multica-ai/multica-backend}"
WEB_REPO="${WEB_REPO:-ghcr.io/multica-ai/multica-web}"
BACKEND_IMAGE="$BACKEND_REPO:$IMAGE_TAG"
WEB_IMAGE="$WEB_REPO:$IMAGE_TAG"
BACKEND_PORT="$(read_env_value BACKEND_PORT || true)"
FRONTEND_PORT="$(read_env_value FRONTEND_PORT || true)"
BACKEND_PORT="${BACKEND_PORT:-3702}"
FRONTEND_PORT="${FRONTEND_PORT:-3180}"
valid_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && (( 10#$1 > 0 && 10#$1 <= 65535 )); }
valid_port "$BACKEND_PORT" || die "invalid BACKEND_PORT in $ENV_FILE"
valid_port "$FRONTEND_PORT" || die "invalid FRONTEND_PORT in $ENV_FILE"

# Reclaim dangling image layers/build cache before the space gate (M-426).
# Dangling images are untagged intermediates not referenced by any container or
# tagged release; pruning them is safe and prevents repeated 10 GiB check failures.
if command -v podman >/dev/null 2>&1; then
  pruned="$(podman image prune -f 2>/dev/null || true)"
  if [ -n "$pruned" ]; then
    log "pruned dangling images before free-space check"
  fi
fi

for path in "$CONTAINERS_ROOT" "$WORKSPACE_ROOT"; do
  [ -d "$path" ] || die "required mount is missing: $path"
  available="$(free_kb "$path")"
  [[ "$available" =~ ^[0-9]+$ ]] || die "could not read free space for $path"
  [ "$available" -ge "$MIN_FREE_KB" ] || die "mount $path has only $((available / 1024 / 1024)) GiB free (needs 10 GiB)"
done

for image in "$BACKEND_IMAGE" "$WEB_IMAGE"; do
  podman image exists "$image" || die "staged image is missing: $image"
  image_commit="$(podman image inspect --format '{{ index .Labels "org.opencontainers.image.revision" }}' "$image" 2>/dev/null || true)"
  [ "$image_commit" = "$EXPECTED_COMMIT" ] || die "$image has revision label '$image_commit', expected $EXPECTED_COMMIT"
  image_tag="$(podman image inspect --format '{{ index .Labels "org.opencontainers.image.version" }}' "$image" 2>/dev/null || true)"
  [ "$image_tag" = "$IMAGE_TAG" ] || die "$image has version label '$image_tag', expected $IMAGE_TAG"
done

export TMPDIR="${MULTICA_MAIN_TMPDIR:-$WORKSPACE_ROOT/tmp/multica-main-deploy}"
case "$TMPDIR" in "$WORKSPACE_ROOT"/*) ;; *) die "TMPDIR must be under $WORKSPACE_ROOT" ;; esac
case "$BACKUP_ROOT" in "$WORKSPACE_ROOT"/*) ;; *) die "backup directory must be under $WORKSPACE_ROOT" ;; esac

printf 'Main Multica deployment plan\n'
printf '  image tag:       %s\n' "$IMAGE_TAG"
printf '  expected commit: %s\n' "$EXPECTED_COMMIT"
printf '  current tag:     %s\n' "$OLD_TAG"
printf '  max drain wait:  %ss\n' "$MAX_DRAIN_SECONDS"
printf '  force:           %s\n' "$([ "$FORCE" = 1 ] && printf true || printf false)"
printf '  backend image:   %s\n' "$BACKEND_IMAGE"
printf '  web image:       %s\n' "$WEB_IMAGE"
printf '  backend port:    %s\n' "$BACKEND_PORT"
printf '  frontend port:   %s\n' "$FRONTEND_PORT"
printf '  backups:         %s\n' "$BACKUP_ROOT"
printf '  switch: stop daemon, update .env, recreate the stack through systemd, verify health/migrations/web, install daemon binary, start daemon\n'
printf '  rollback: restore .env and old daemon binary, recreate the stack through systemd, start daemon; database dump is retained, never restored automatically\n'
if [ "$DRY_RUN" = 1 ]; then
  log "dry-run complete; no stack state was changed"
  exit 0
fi

active_count="$(daemon_active_count)" || die "could not read numeric active_task_count from daemon status (5 tries)"
force_json="$([ "$FORCE" = 1 ] && printf true || printf false)"
# max-drain-seconds 0 (the CI/CD default, owner directive 2026-11-04) stays the
# explicit cancel-without-force path: no lease is requested and the stop below
# still cancels tasks. A positive value takes an authenticated drain lease so
# claim paths are paused and the fleet can actually reach idle.
DRAIN_OUTCOME="no_drain"
if [ "$MAX_DRAIN_SECONDS" -gt 0 ]; then
  if ! DRAIN_LEASE_TOKEN="$(request_drain)"; then
    die "could not request an authenticated drain lease; no daemon changes made"
  fi
  audit_stop drain_begin pending "$DRAIN_LEASE_TIMEOUT" "$force_json" || true
  # set +e: a non-zero wait result is data here, not an errexit condition.
  set +e
  wait_for_drain "$DRAIN_LEASE_TOKEN"
  drain_rc=$?
  set -e
  case "$drain_rc" in
    0)
      DRAIN_OUTCOME="drained"
      log "drain complete; claim paths stay paused through the stop"
      ;;
    1)
      # Timeout behaviour is unchanged by this lease: unforced is an abort
      # with no changes; with --force the operator explicitly cancels tasks.
      drain_abort_best_effort
      if [ "$FORCE" != 1 ]; then
        die "drain timed out with $active_count active task(s); no changes made (pass --force to cancel them)"
      fi
      DRAIN_OUTCOME="force_cancel"
      log "drain timed out; force authorizes canceling remaining task(s)"
      ;;
    *)
      drain_abort_best_effort
      die "lost control of the drain lease; no changes made"
      ;;
  esac
fi

mkdir -p "$TMPDIR" "$BACKUP_ROOT"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="$(mktemp -d "$BACKUP_ROOT/${STAMP}.XXXXXX")"
chmod 700 "$BACKUP_DIR"
cp -p "$ENV_FILE" "$BACKUP_DIR/.env"
cp -p "$HOME/.local/bin/multica" "$BACKUP_DIR/multica"

rollback() {
  local rc=$?
  trap - EXIT
  set +e
  [ -z "$ENV_TMP" ] || rm -f "$ENV_TMP"
  if [ "$SWITCH_STARTED" = 1 ] && [ "$ROLLBACK_ACTIVE" = 0 ]; then
    ROLLBACK_ACTIVE=1
    local rollback_failed=0
    log "deployment failed; rolling back environment and daemon binary"
    audit_stop rollback_stop rollback "$MAX_DRAIN_SECONDS" "$force_json" || true
    systemctl --user stop multica-daemon.service || rollback_failed=1
    cp -p "$BACKUP_DIR/.env" "$ENV_FILE" || rollback_failed=1
    install -m 755 "$BACKUP_DIR/multica" "$HOME/.local/bin/multica" || rollback_failed=1
    stack_up || rollback_failed=1
    systemctl --user start multica-daemon.service || rollback_failed=1
    if [ "$rollback_failed" = 0 ]; then
      log "rollback commands completed; database dump retained at $BACKUP_DIR/database.dump"
    else
      log "ROLLBACK FAILED; inspect the stack; database dump retained at $BACKUP_DIR/database.dump"
    fi
  elif [ "$DAEMON_WAS_STOPPED" = 1 ]; then
    systemctl --user start multica-daemon.service || log "FAILED to restart the daemon after pre-switch abort"
  fi
  post_run || true
  exit "$rc"
}
trap rollback EXIT

if [ "$DRAIN_OUTCOME" = "no_drain" ] && [ "$active_count" -gt 0 ]; then
  log "stopping daemon to cancel $active_count remaining task(s); deploy does not wait for task drain"
fi
audit_stop stop "$DRAIN_OUTCOME" "$MAX_DRAIN_SECONDS" "$force_json" || true
DAEMON_WAS_STOPPED=1
systemctl --user stop multica-daemon.service
db_container="$(cd "$STACK_DIR" && podman ps -q --filter "label=com.docker.compose.service=postgres" --filter "label=com.docker.compose.project=multica")" || die "could not locate postgres container"
[ -n "$db_container" ] || die "postgres container is not running"
podman exec "$db_container" sh -c 'pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"' > "$BACKUP_DIR/database.dump" || die "database backup failed"
[ -s "$BACKUP_DIR/database.dump" ] || die "database dump is empty"
chmod 600 "$BACKUP_DIR/.env" "$BACKUP_DIR/multica" "$BACKUP_DIR/database.dump"
log "backup complete: $BACKUP_DIR (database dump will not be restored automatically)"
SWITCH_STARTED=1
ENV_TMP="$ENV_FILE.tmp.$$"
awk -F= -v tag="$IMAGE_TAG" '
  BEGIN {updated=0}
  $1 == "MULTICA_IMAGE_TAG" {print "MULTICA_IMAGE_TAG=" tag; updated++; next}
  {print}
  END {if (updated != 1) exit 2}
' "$ENV_FILE" > "$ENV_TMP" || die "could not update MULTICA_IMAGE_TAG"
chmod --reference="$ENV_FILE" "$ENV_TMP"
mv "$ENV_TMP" "$ENV_FILE"
ENV_TMP=""
stack_up

health_ok=0
for _ in $(seq 1 60); do
  health="$(curl -fsS --max-time 3 "http://127.0.0.1:$BACKEND_PORT/health" 2>/dev/null || true)"
  commit="$(jq -r '.commit // empty' <<<"$health" 2>/dev/null || true)"
  version="$(jq -r '.version // empty' <<<"$health" 2>/dev/null || true)"
  if [ "$commit" = "$EXPECTED_COMMIT" ] && [ "$version" = "$IMAGE_TAG" ]; then health_ok=1; break; fi
  sleep 2
done
[ "$health_ok" = 1 ] || die "/health did not report expected commit and version"

ready_ok=0
for _ in $(seq 1 60); do
  ready="$(curl -fsS --max-time 3 "http://127.0.0.1:$BACKEND_PORT/healthz" 2>/dev/null || true)"
  ready_status="$(jq -r '.status // empty' <<<"$ready" 2>/dev/null || true)"
  migrations="$(jq -r '.checks.migrations // empty' <<<"$ready" 2>/dev/null || true)"
  if [ "$ready_status" = "ok" ] && [ "$migrations" = "ok" ]; then ready_ok=1; break; fi
  sleep 2
done
[ "$ready_ok" = 1 ] || die "/healthz did not report status=ok and checks.migrations=ok"

web_ok=0
# The web UI redirects unauthenticated / to /login (302), so follow redirects
# and require the final response to be 200 -- a bare 200 check fails on every
# deploy even when the stack is healthy.
for _ in $(seq 1 30); do
  if curl -fsSL --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$FRONTEND_PORT/" | grep -qx 200; then web_ok=1; break; fi
  sleep 2
done
[ "$web_ok" = 1 ] || die "web did not return HTTP 200"

# M-556 option C: the host side of each published port must answer, not just
# the in-container healthchecks. A dead rootlessport forwarder looks healthy
# from inside while the host port is dead. The probe may restart one
# forwarder-dead container (rate-limited); it runs with the deploy lock held,
# so it reports instead of skipping (PORT_PROBE_SKIP_LOCK=1).
log "probing host-side published ports"
PORT_PROBE_SKIP_LOCK=1 bash "$(dirname "${BASH_SOURCE[0]}")/stack-port-probe.sh" "$STACK_DIR" \
  || die "host-side port probe failed after the switch; see above"

# M-556: a backend that froze right after startup answers neither the host
# nor the in-container probe, yet "started" fine — prove it is actually
# serving by requiring a /healthz request in its log within 30s. The compose
# healthcheck hits :8080/healthz every 5s, so a serving backend always leaves
# this trace quickly after a switch.
log "waiting for the backend to log a /healthz request (serving proof)"
be_cid="$(podman ps -q \
  --filter "label=com.docker.compose.project=$(basename "$STACK_DIR")" \
  --filter 'label=com.docker.compose.service=backend' | head -1)"
[ -n "$be_cid" ] || die "could not find the backend container after the switch"
served=0
for _ in $(seq 1 15); do
  if podman logs --tail 100 "$be_cid" 2>&1 | grep -q 'healthz'; then served=1; break; fi
  sleep 2
done
[ "$served" = 1 ] || die "backend logged no /healthz request within 30s of the switch; not provably serving"

new_binary="$TMPDIR/multica.new"
podman run --rm --network none --entrypoint cat "$BACKEND_IMAGE" /app/multica > "$new_binary"
[ -s "$new_binary" ] || die "new daemon binary extraction produced an empty file"
chmod 755 "$new_binary"
install -m 755 "$new_binary" "$HOME/.local/bin/multica.new"
mv "$HOME/.local/bin/multica.new" "$HOME/.local/bin/multica"
systemctl --user start multica-daemon.service
DAEMON_WAS_STOPPED=0

daemon_ok=0
for _ in $(seq 1 30); do
  daemon_json="$(multica daemon status --output json 2>/dev/null || true)"
  daemon_version="$(jq -r '.cli_version // empty' <<<"$daemon_json" 2>/dev/null || true)"
  if [ "$daemon_version" = "$IMAGE_TAG" ]; then daemon_ok=1; break; fi
  sleep 2
done
[ "$daemon_ok" = 1 ] || die "daemon status did not report version $IMAGE_TAG"

trap - EXIT
post_run
log "deployment verified: commit=$EXPECTED_COMMIT version=$IMAGE_TAG; backup=$BACKUP_DIR"

# Image retention (owner directive, 2026-11-04): keep only the two newest
# localhost/multica-{web,backend} versions — the one just deployed and its
# predecessor for rollback. podman rmi refuses images a container uses, so
# the running stack is never touched; a refused removal is logged, never
# fatal, since the deployment already succeeded.
for repo in localhost/multica-web localhost/multica-backend; do
  while read -r image; do
    [ -n "$image" ] || continue
    if ! podman rmi "$image" >/dev/null 2>&1; then
      log "image retention kept $image (in use or already removed)"
    fi
  done < <(podman images --format '{{.Repository}} {{.Tag}} {{.CreatedAt}}' \
    | awk -v repo="$repo" '$1 == repo && $2 != "<none>"' \
    | sort -k3,3r -k4,4r \
    | awk 'NR > 2 { print $1 ":" $2 }')
done
