#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/deploy-main.sh"
SANDBOX="$(mktemp -d "$REPO_ROOT/.deploy-main-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT
STUB_BIN="$SANDBOX/bin"
HOME_DIR="$SANDBOX/home"
STACK_DIR="$HOME_DIR/.local/share/multica"
WORKSPACE_ROOT="$SANDBOX/workspace"
CONTAINERS_ROOT="$SANDBOX/containers"
mkdir -p "$STUB_BIN" "$HOME_DIR/.local/bin" "$HOME_DIR/.multica" "$STACK_DIR" "$WORKSPACE_ROOT" "$CONTAINERS_ROOT"
printf '{"port":19514,"token":"ctl-test-token"}\n' > "$HOME_DIR/.multica/daemon-drain.json"
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"
printf 'services: {}\n' > "$STACK_DIR/compose.yml"
printf 'old daemon binary\n' > "$HOME_DIR/.local/bin/multica"
chmod 755 "$HOME_DIR/.local/bin/multica"

cat > "$STUB_BIN/ps" <<'EOF'
#!/usr/bin/env bash
printf '1 %s\n' "${PARENT_ARGS:-init}"
EOF
cat > "$STUB_BIN/df" <<'EOF'
#!/usr/bin/env bash
free_kb=20000000
if [[ -n "${LOW_SPACE_PATH:-}" && "${LOW_SPACE_PATH}" == "$2" ]]; then free_kb="${LOW_FREE_KB:-10000000}"; fi
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/fake 20000000 1 %s 1%% /fake\n' "$free_kb"
EOF
cat > "$STUB_BIN/podman" <<'EOF'
#!/usr/bin/env bash
# Every podman invocation must run with an empty RUNNER_TRACKING_ID (the
# deploy and probe clear it); a leaked job id here fails the assertion below.
printf 'tracked:%s\n' "${RUNNER_TRACKING_ID-<unset>}" >> "$STUB_LOG"
# Simulate conmon: one detached child that outlives the deploy and inherits
# every open fd. If the deploy held the lock on an fd, this child keeps it
# locked after the script exits and the assertion below fails.
if [ "${LEAK_SLEEPER:-0}" = 1 ] && [ ! -e "$STUB_LOG.spawned" ]; then
  : > "$STUB_LOG.spawned"
  setsid /bin/sleep 30 < /dev/null > /dev/null 2>&1 &
fi
case "$1 $2" in
  'image exists') [[ "${FAIL_IMAGE:-}" != "$3" ]] ;;
  'image inspect')
    case "$4" in
      *revision*) printf '%s\n' "$EXPECTED_COMMIT" ;;
      *version*) printf '%s\n' "$IMAGE_TAG" ;;
      *) exit 2 ;;
    esac
    ;;
  'ps -q') printf 'ps -q:%s\n' "$PWD" >> "$STUB_LOG"; printf 'db-id\n' ;;
  'compose up')
    printf 'compose up\n' >> "$STUB_LOG" ;;
  'compose -f') printf 'cid-probe-stub\n' ;;
  'exec db-id') printf 'database dump bytes\n' ;;
  'exec cid-probe-stub') exit 0 ;;
  'logs '*)
    printf 'podman logs:%s\n' "$*" >> "$STUB_LOG"
    # M-615: emulate a busy backend. One /healthz line is written early (the
    # 5s compose healthcheck), then 3000 daemon API lines (~78 KiB, well past
    # the 64 KiB pipe buffer) bury it. `--tail 100` shows only the noise (the
    # M-615 bug); `--since` reads the whole window since the switch and still
    # finds the healthz line.
    # M-632: the volume also reproduces the SIGPIPE defect — a reader that
    # exits at the first match (`grep -q`) makes the writer fail with 141 under
    # pipefail. NO_HEALTHZ_BACKEND_LOG drops the line entirely so the proof
    # must still fail on a large stream.
    backend_log="$STUB_LOG.backend"
    if [ "${NO_HEALTHZ_BACKEND_LOG:-0}" = 1 ]; then
      for i in $(seq 1 3000); do printf 'GET /api/daemon/tasks 200\n'; done > "$backend_log"
    elif [ "${NOISY_BACKEND_LOG:-0}" = 1 ]; then
      {
        printf 'GET /healthz 200\n'
        for i in $(seq 1 3000); do printf 'GET /api/daemon/tasks 200\n'; done
      } > "$backend_log"
    else
      printf 'GET /healthz 200\n' > "$backend_log"
    fi
    case "$2" in
      --since) cat "$backend_log" ;;
      --tail) tail -n "${3:-100}" "$backend_log" ;;
      *) cat "$backend_log" ;;
    esac
    ;;
  'run --rm') printf 'new daemon binary\n' ;;
  'images --format')
    # Lines look like: <repo> <tag> <date> <time> <tz> UTC. Empty by default
    # (nothing to prune); the retention test overrides this.
    if [ -n "${PODMAN_IMAGES_LINES:-}" ]; then printf '%s\n' "$PODMAN_IMAGES_LINES"; fi
    ;;
  'rmi '*) printf 'rmi:%s\n' "$2" >> "$STUB_LOG" ;;
  *) printf 'unexpected podman call: %s\n' "$*" >&2; exit 90 ;;
esac
EOF
cat > "$STUB_BIN/multica" <<'EOF'
#!/usr/bin/env bash
version="$IMAGE_TAG"
if [[ "${FAIL_DAEMON_VERSION:-0}" == "1" ]]; then version=wrong-version; fi
if [[ "${ACTIVE_TASK_COUNT:-0}" == "1" ]]; then
  printf '{"active_task_count":1,"cli_version":"%s"}\n' "$version"
else
  printf '{"active_task_count":0,"cli_version":"%s"}\n' "$version"
fi
EOF
cat > "$STUB_BIN/systemctl" <<'EOF'
#!/usr/bin/env bash
printf 'systemctl %s\n' "$*" >> "$STUB_LOG"
EOF
cat > "$STUB_BIN/curl" <<'EOF'
#!/usr/bin/env bash
url="${*: -1}"
printf 'curl %s\n' "$url" >> "$STUB_LOG"
case "$url" in
  */health)
    if [[ "${FAIL_HEALTH:-0}" == "1" ]]; then
      printf '{"status":"ok","commit":"old","version":"old-tag"}\n'
    else
      printf '{"status":"ok","commit":"%s","version":"%s"}\n' "$EXPECTED_COMMIT" "$IMAGE_TAG"
    fi
    ;;
  */healthz) printf '{"status":"ok","checks":{"db":"ok","migrations":"ok"}}\n' ;;
  */drain/begin)
    if [[ "${FAIL_DRAIN_BEGIN:-0}" == "1" ]]; then exit 22; fi
    printf '{"status":"draining","token":"lease-test","expires_at":"2026-11-27T12:00:00Z"}\n'
    ;;
  */drain/status)
    if [[ "${FAIL_DRAIN_STATUS:-0}" == "1" ]]; then exit 22; fi
    if [[ "${ACTIVE_TASK_COUNT:-0}" == "1" ]]; then
      printf '{"status":"draining","token":"lease-test","active_task_count":1,"claims_in_flight":0}\n'
    else
      printf '{"status":"drained","token":"lease-test","active_task_count":0,"claims_in_flight":0}\n'
    fi
    ;;
  */drain/abort) printf '{"status":"aborted","token":"lease-test"}\n' ;;
  */login) printf 'ok' ;;
  */) printf '200' ;;
  *) exit 91 ;;
esac
EOF
cat > "$STUB_BIN/sleep" <<'EOF'
#!/usr/bin/env bash
/bin/sleep 0.05
EOF
for command in ps df podman multica systemctl curl sleep; do chmod 755 "$STUB_BIN/$command"; done

export PATH="$STUB_BIN:$PATH"
export HOME="$HOME_DIR"
export MULTICA_MAIN_STACK_DIR="$STACK_DIR"
export MULTICA_MAIN_WORKSPACE_ROOT="$WORKSPACE_ROOT"
export MULTICA_MAIN_CONTAINERS_ROOT="$CONTAINERS_ROOT"
export MULTICA_MAIN_BACKUP_ROOT="$WORKSPACE_ROOT/backups"
export MULTICA_MAIN_TMPDIR="$WORKSPACE_ROOT/tmp/multica-main-deploy"
export MULTICA_MAIN_PROC_ROOT="$SANDBOX/proc"
export MULTICA_DEPLOY_STOP_AUDIT_LOG="$WORKSPACE_ROOT/multica-main-stop-audit.log"
AUDIT_LOG="$MULTICA_DEPLOY_STOP_AUDIT_LOG"
export PORT_PROBE_STATE_DIR="$SANDBOX/probe-state"
export RUNNER_TRACKING_ID=test-job-id-1234
export STUB_LOG="$SANDBOX/commands.log"
export IMAGE_TAG=test-deploy
export EXPECTED_COMMIT=0123456789abcdef0123456789abcdef01234567

assert() { "$@" || { printf 'assertion failed: %s\n' "$*" >&2; exit 1; }; }
audit_has() { jq -es "$1" "$AUDIT_LOG" >/dev/null; }
run() { bash "$SCRIPT" "$IMAGE_TAG" "$EXPECTED_COMMIT" 1 "$@"; }

# Dry-run performs read-only preflight and does not make temporary or backup dirs.
output="$(run --dry-run)"
[[ "$output" == *"dry-run complete"* ]]
assert test ! -e "$MULTICA_MAIN_TMPDIR"
assert test ! -e "$MULTICA_MAIN_BACKUP_ROOT"

# M-632: on the deploy runner `multica` lives only in $HOME/.local/bin, which
# the script appends to PATH itself. Before #386 the preflight died with
# "required command not found: multica". PATH here is a copy of the stub bin
# with multica removed plus the system dirs, so only the script's own append
# can satisfy the check.
NOMULTICA_BIN="$SANDBOX/bin-nomultica"
cp -a "$STUB_BIN" "$NOMULTICA_BIN"
rm -f "$NOMULTICA_BIN/multica"
cat > "$HOME/.local/bin/multica" <<'EOF'
#!/usr/bin/env bash
printf '{"active_task_count":0,"cli_version":"%s"}\n' "${IMAGE_TAG:-}"
EOF
chmod 755 "$HOME/.local/bin/multica"
if ! output="$(PATH="$NOMULTICA_BIN:/usr/bin:/bin" bash "$SCRIPT" "$IMAGE_TAG" "$EXPECTED_COMMIT" 1 --dry-run 2>&1)"; then
  echo "expected dry-run to find multica in \$HOME/.local/bin" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi
[[ "$output" == *"dry-run complete"* ]]
# Restore the non-script fixture the later cases assert on.
printf 'old daemon binary\n' > "$HOME/.local/bin/multica"
chmod 755 "$HOME/.local/bin/multica"

# Missing staged image fails during preflight, before any state changes.
if FAIL_IMAGE="ghcr.io/multica-ai/multica-web:$IMAGE_TAG" run --dry-run >/dev/null 2>&1; then
  echo "expected missing-image preflight failure" >&2; exit 1
fi
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"

# The production ancestry guard rejects daemon-managed execution before work.
if PARENT_ARGS='multica daemon start' run --dry-run >/dev/null 2>&1; then
  echo "expected daemon ancestry guard" >&2; exit 1
fi
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"

# Drain timeout aborts by default and leaves config and daemon untouched.
if ACTIVE_TASK_COUNT=1 run >/dev/null 2>&1; then
  echo "expected drain timeout" >&2; exit 1
fi
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
assert grep -q 'old daemon binary' "$HOME/.local/bin/multica"

# drain_timeout_seconds=0 (the CI/CD default) does not wait for active tasks:
# it cancels them and deploys immediately (owner directive, 2026-11-04). No
# lease is requested in this explicit cancel path.
drain0_log="$SANDBOX/drain0.log"
: > "$AUDIT_LOG"
before_begin="$(grep -c '/drain/begin$' "$STUB_LOG" || true)"
ACTIVE_TASK_COUNT=1 bash "$SCRIPT" "$IMAGE_TAG" "$EXPECTED_COMMIT" 0 >"$drain0_log" 2>&1 || {
  echo "expected drain=0 deploy to succeed with active tasks" >&2; cat "$drain0_log" >&2; exit 1; }
assert grep -q 'deploy does not wait for task drain' "$drain0_log"
assert grep -q "MULTICA_IMAGE_TAG=$IMAGE_TAG" "$STACK_DIR/.env"
assert grep -q "new daemon binary" "$HOME/.local/bin/multica"
assert test "$(grep -c '/drain/begin$' "$STUB_LOG")" -eq "$before_begin"
assert audit_has 'any(.[]; .operation=="stop" and .outcome=="no_drain")'
# Restore fixtures the later rollback tests rely on.
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"
printf 'old daemon binary\n' > "$HOME/.local/bin/multica"

# A positive drain takes an authenticated lease, verifies idle through it and
# records the stop with structured UTC fields. M-632: the status URL must carry
# the port from the daemon's control file — before #386 request_drain ran in a
# subshell, lost DRAIN_PORT and called `http://127.0.0.1:/drain/status`.
: > "$AUDIT_LOG"
before_begin="$(grep -c '/drain/begin$' "$STUB_LOG" || true)"
before_status="$(grep -c '^curl http://127.0.0.1:19514/drain/status$' "$STUB_LOG" || true)"
run >/dev/null 2>&1
assert test "$(grep -c '/drain/begin$' "$STUB_LOG")" -eq "$((before_begin + 1))"
assert test "$(grep -c '^curl http://127.0.0.1:19514/drain/status$' "$STUB_LOG")" -eq "$((before_status + 1))"
assert audit_has 'any(.[]; .operation=="stop" and .outcome=="drained" and .unit=="multica-daemon.service" and (.force|type=="boolean") and (.timeout|type=="number") and (.ts|test("Z$")))'
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"
printf 'old daemon binary\n' > "$HOME/.local/bin/multica"

# An unforced timeout aborts the lease and resumes claims; nothing is switched.
# The abort must reach the same control port, with the lease token in its
# header (M-632: a subshell lost DRAIN_PORT and called `127.0.0.1:/drain/abort`).
: > "$AUDIT_LOG"
before_abort="$(grep -c '^curl http://127.0.0.1:19514/drain/abort$' "$STUB_LOG" || true)"
before_status="$(grep -c '^curl http://127.0.0.1:19514/drain/status$' "$STUB_LOG" || true)"
before_stops="$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG" || true)"
if ACTIVE_TASK_COUNT=1 run >/dev/null 2>&1; then
  echo "expected unforced drain timeout to abort" >&2; exit 1
fi
assert test "$(grep -c '^curl http://127.0.0.1:19514/drain/abort$' "$STUB_LOG")" -eq "$((before_abort + 1))"
assert test "$(grep -c '^curl http://127.0.0.1:19514/drain/status$' "$STUB_LOG")" -gt "$before_status"
assert test "$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG")" -eq "$before_stops"
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
assert audit_has 'all(.[]; .operation != "stop")'

# With --force the same timeout aborts the lease and then explicitly cancels
# the remaining tasks, which is the recorded 2026-11-04 behaviour.
: > "$AUDIT_LOG"
before_abort="$(grep -c '/drain/abort$' "$STUB_LOG" || true)"
if ! ACTIVE_TASK_COUNT=1 bash "$SCRIPT" "$IMAGE_TAG" "$EXPECTED_COMMIT" 1 --force >/dev/null 2>&1; then
  echo "expected forced drain timeout deploy to succeed" >&2; exit 1
fi
assert test "$(grep -c '/drain/abort$' "$STUB_LOG")" -eq "$((before_abort + 1))"
assert audit_has 'any(.[]; .operation=="stop" and .outcome=="force_cancel" and .force==true)'
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"
printf 'old daemon binary\n' > "$HOME/.local/bin/multica"

# Losing control of the drain (no credential or a lost status endpoint) aborts
# and stops nothing.
for failure in FAIL_DRAIN_BEGIN=1 FAIL_DRAIN_STATUS=1; do
  before_stops="$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG" || true)"
  if env "$failure" run >/dev/null 2>&1; then
    echo "expected lost-control drain to abort ($failure)" >&2; exit 1
  fi
  assert test "$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG")" -eq "$before_stops"
  assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
done

# A post-switch health failure restores .env and daemon binary and brings the
# compose stack and daemon back up. All host commands above are stubbed.
before_restarts="$(grep -c '^systemctl --user restart multica.service$' "$STUB_LOG")"
: > "$AUDIT_LOG"
if FAIL_HEALTH=1 run >/dev/null 2>&1; then
  echo "expected health verification failure" >&2; exit 1
fi
# Both the planned stop and the rollback stop are audited before they run.
assert audit_has 'any(.[]; .operation=="stop" and .outcome=="drained")'
assert audit_has 'any(.[]; .operation=="rollback_stop" and .outcome=="rollback")'
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
assert grep -q 'old daemon binary' "$HOME/.local/bin/multica"
assert grep -q 'systemctl --user start multica-daemon.service' "$STUB_LOG"
# The stack goes up through the persistent multica.service unit (once in the
# failed deploy, once in its rollback), never through the job's shell, so
# conmon can never inherit the runner's tracking id.
assert test "$(grep -c '^systemctl --user restart multica.service$' "$STUB_LOG")" -eq "$((before_restarts + 2))"
assert grep -q "ps -q:$STACK_DIR" "$STUB_LOG"
dump="$(find "$MULTICA_MAIN_BACKUP_ROOT" -name database.dump -type f -print -quit)"
assert test -s "$dump"

# A daemon-version failure happens after start; rollback must stop that daemon
# before restoring the old binary, then bring the old stack and daemon back.
before_stops="$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG")"
if FAIL_DAEMON_VERSION=1 run >/dev/null 2>&1; then
  echo "expected daemon version verification failure" >&2; exit 1
fi
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
assert grep -q 'old daemon binary' "$HOME/.local/bin/multica"
assert grep -q 'curl http://127.0.0.1:4802/health' "$STUB_LOG"
assert grep -q 'curl http://127.0.0.1:4802/healthz' "$STUB_LOG"
assert grep -q 'curl http://127.0.0.1:4180/' "$STUB_LOG"
after_stops="$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG")"
assert test "$after_stops" -gt "$before_stops"

# M-615: on a busy stack the 5s /healthz line is pushed out of the last 100
# log lines. The serving proof must read logs since the switch (--since), not
# a fixed --tail window, or a healthy deploy rolls back. The stub above drops
# the healthz line for --tail and keeps it for --since, so a regression to
# --tail makes this deploy fail.
# M-632: the same stub writes the healthz line first and then ~78 KiB, past the
# 64 KiB pipe buffer. A `grep -q` reader exits at the first match, the writer
# takes SIGPIPE and pipefail turns a found line into exit 141 (runs 46147975031
# and 46149857404 rolled a healthy stack back this way). The deploy must pass.
noisy_log="$SANDBOX/noisy-serving-proof.log"
if ! NOISY_BACKEND_LOG=1 run >"$noisy_log" 2>&1; then
  echo "expected noisy-log deploy to pass the serving proof" >&2
  cat "$noisy_log" >&2
  exit 1
fi
assert grep -q 'waiting for the backend to log a /healthz request' "$noisy_log"
assert grep -q 'podman logs:.*--since' "$STUB_LOG"
assert test "$(wc -c < "$STUB_LOG.backend")" -gt 65536
# Restore fixtures the later rollback tests rely on.
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"
printf 'old daemon binary\n' > "$HOME/.local/bin/multica"

# M-632: a backend that never logs /healthz must still fail the proof and roll
# back, even with a log large enough to fill the pipe buffer — the fix counts
# matches, it does not merely ignore the pipeline status.
before_stops="$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG" || true)"
nohealthz_log="$SANDBOX/no-healthz-serving-proof.log"
if NO_HEALTHZ_BACKEND_LOG=1 run >"$nohealthz_log" 2>&1; then
  echo "expected the serving proof to fail without a /healthz line" >&2
  exit 1
fi
assert grep -q 'not provably serving' "$nohealthz_log"
assert test "$(grep -c 'systemctl --user stop multica-daemon.service' "$STUB_LOG")" -gt "$before_stops"
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
assert grep -q 'old daemon binary' "$HOME/.local/bin/multica"

# Image retention: after a successful deploy, only the two newest
# localhost/multica-{web,backend} versions stay; older tags are removed and
# other repositories are untouched.
before_rmis="$(grep -c '^rmi:' "$STUB_LOG" || true)"
PODMAN_IMAGES_LINES="$(printf '%s\n' \
  'localhost/multica-web keep-newest 2026-11-04 10:00:00 +0000 UTC' \
  'localhost/multica-web keep-previous 2026-11-03 10:00:00 +0000 UTC' \
  'localhost/multica-web drop-old 2026-11-02 10:00:00 +0000 UTC' \
  'localhost/multica-backend keep-b-newest 2026-11-04 11:00:00 +0000 UTC' \
  'localhost/multica-backend keep-b-previous 2026-11-03 11:00:00 +0000 UTC' \
  'localhost/multica-backend drop-b-old 2026-11-02 11:00:00 +0000 UTC' \
  'ghcr.io/multica-ai/multica-web latest 2026-10-01 10:00:00 +0000 UTC')" run >/dev/null 2>&1
assert grep -q '^rmi:localhost/multica-web:drop-old$' "$STUB_LOG"
assert grep -q '^rmi:localhost/multica-backend:drop-b-old$' "$STUB_LOG"
assert test "$(grep -c '^rmi:' "$STUB_LOG")" -eq "$((before_rmis + 2))"
# The successful deploy above rewrote MULTICA_IMAGE_TAG; restore it so later
# failure-path assertions see the original stack state.
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"

# A low-space preflight failure names the mount and its free GiB, for both
# mounts, and leaves the stack untouched.
err="$(LOW_SPACE_PATH="$WORKSPACE_ROOT" LOW_FREE_KB=9700000 run --dry-run 2>&1 || true)"
[[ "$err" == *"mount $WORKSPACE_ROOT has only 9 GiB free (needs 10 GiB)"* ]]
err="$(LOW_SPACE_PATH="$CONTAINERS_ROOT" LOW_FREE_KB=5242879 run --dry-run 2>&1 || true)"
[[ "$err" == *"mount $CONTAINERS_ROOT has only 4 GiB free (needs 10 GiB)"* ]]
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"

# A successful deploy empties the runner TMPDIR and reports free space per
# mount after the run.
mkdir -p "$MULTICA_MAIN_TMPDIR"
printf 'stale\n' > "$MULTICA_MAIN_TMPDIR/stale.bin"
output="$(run)"
[[ "$output" == *"emptied runner TMPDIR: $MULTICA_MAIN_TMPDIR"* ]]
[[ "$output" == *"mount $WORKSPACE_ROOT has "*' GiB free'* ]]
[[ "$output" == *"mount $CONTAINERS_ROOT has "*' GiB free'* ]]
assert test -z "$(ls -A "$MULTICA_MAIN_TMPDIR")"

# The deploy lock is never left held: the compose-up stub starts a detached
# child (simulating conmon, which outlives the deploy) while LEAK_SLEEPER=1.
# With the old `exec 200>` + `flock -n 200` pattern that child inherited the
# lock fd, so the very next flock here failed; with the re-exec + `flock -o`
# no descendant ever sees the fd.
LOCK_FILE="$WORKSPACE_ROOT/multica-main-deploy.lock"
LEAK_SLEEPER=1 run >/dev/null 2>&1
# The 30s sleeper still runs here; if it inherited the lock the next line fails.
assert flock -n "$LOCK_FILE" true
# No podman call may carry the job id: RUNNER_TRACKING_ID is cleared for the
# whole deploy (stub records every invocation's value).
assert test -z "$(grep -h '^tracked:' "$STUB_LOG" | grep -v '^tracked:$' || true)"
# Reset the fixture: the leak run above is a successful deploy (new tag).
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"

# A second deploy while the lock is held refuses with a clear message and
# changes nothing (no re-exec recursion: the child sees DEPLOY_MAIN_LOCKED).
(
  exec 202>"$LOCK_FILE"
  flock -n 202 || exit 1
  /bin/sleep 30
) &
locker=$!
/bin/sleep 0.5
err="$(run 2>&1 || true)"
[[ "$err" == *"another deploy holds the lock"* ]]
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"
kill "$locker" 2>/dev/null || true
wait "$locker" 2>/dev/null || true

# FF_AGENT_ROLE_TYPE survives the MULTICA_IMAGE_TAG rewrite (M-539): the awk
# update only touches the tag line, so a production flag set in the stack .env
# is not wiped by a deploy.
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\nFF_AGENT_ROLE_TYPE=true\n' > "$STACK_DIR/.env"
run >/dev/null 2>&1
assert grep -q '^FF_AGENT_ROLE_TYPE=true$' "$STACK_DIR/.env"
assert grep -q "MULTICA_IMAGE_TAG=$IMAGE_TAG" "$STACK_DIR/.env"
printf 'MULTICA_IMAGE_TAG=old-tag\nPOSTGRES_DB=multica\nBACKEND_PORT=4802\nFRONTEND_PORT=4180\n' > "$STACK_DIR/.env"

# A tag embedding a stale short SHA fails preflight before any state changes
# (M-539: main-0.11.1-7239fad88 shipped commit 4bdc0890c).
if bash "$SCRIPT" "main-0.11.1-000000000" "$EXPECTED_COMMIT" 1 --dry-run >/dev/null 2>&1; then
  echo "expected stale-SHA image tag rejection" >&2; exit 1
fi
assert grep -q '^MULTICA_IMAGE_TAG=old-tag$' "$STACK_DIR/.env"

# A tag embedding the expected commit's short SHA passes preflight.
matching_tag="main-0.11.1-${EXPECTED_COMMIT:0:9}"
IMAGE_TAG="$matching_tag" bash "$SCRIPT" "$matching_tag" "$EXPECTED_COMMIT" 1 --dry-run >/dev/null 2>&1

printf 'deploy-main stub tests passed\n'
