#!/usr/bin/env bash
# scripts/stack-port-probe.test.sh — M-556 option C. Stubs curl, podman and ss
# so the three classifications, the rate limit and the lock skip are
# exercised without touching a real stack.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$REPO_ROOT/scripts/stack-port-probe.sh"
SANDBOX="$(mktemp -d "$REPO_ROOT/.port-probe-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT
STUB_BIN="$SANDBOX/bin"
STACK_DIR="$SANDBOX/stack"
mkdir -p "$STUB_BIN" "$STACK_DIR"
printf 'BACKEND_PORT=4802\nFRONTEND_INTERNAL_PORT=4183\n' > "$STACK_DIR/.env"
printf 'services: {}\n' > "$STACK_DIR/compose.yml"

# Stub control: HOST_OK=1/0 per URL pattern, CONT_OK=1/0, RESTART_LOG file.
# The curl stub simulates healing: once a restart was recorded the host side
# answers again (unless STUCK=1 forces it dead for the rate-limit case).
# podman answers compose ps, exec (wget), port and restart from these.
cat > "$STUB_BIN/curl" <<'EOF'
#!/usr/bin/env bash
url="${*: -1}"
if [ "${STUCK:-0}" = 1 ] && { [[ "$url" == *":4802"* ]] || [[ "$url" == *":4183"* ]]; }; then
  exit 22
fi
if [ -s "$RESTART_LOG" ]; then exit 0; fi
case "$url" in
  *":4802"*) [ "${HOST_BACKEND_OK:-1}" = 1 ] ;;
  *":4183"*) [ "${HOST_FRONTEND_OK:-1}" = 1 ] ;;
  *) exit 22 ;;
esac
EOF
cat > "$STUB_BIN/podman" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = "ps" ]; then
  # label-filtered lookup: last service filter value is the service name.
  svc=""
  for arg in "$@"; do
    case "$arg" in label=com.docker.compose.service=*) svc="${arg##*=}";; esac
  done
  printf 'cid-%s\n' "$svc"
  exit 0
fi
case "$1 $2" in
  "exec "*)
    # Countdown failures apply to the backend only (per-service streaks);
    # every other exec honors the CONT_OK switch.
    if [[ "$2" == cid-backend* ]] && [ -n "${CONT_FAIL_FILE:-}" ] && [ -s "$CONT_FAIL_FILE" ]; then
      n="$(cat "$CONT_FAIL_FILE")"
      if [ "$n" -gt 0 ]; then printf '%s' "$((n - 1))" > "$CONT_FAIL_FILE"; exit 1; fi
    fi
    [ "${CONT_OK:-1}" = 1 ] ;;
  "port "*)
    printf '8080/tcp -> 0.0.0.0:4802\n' ;;
  "restart "*)
    printf '%s\n' "$2" >> "$RESTART_LOG" ;;
  "top "*)
    printf 'PID USER TIME COMMAND\n4242 root 1:00 server\n' >> "$CALL_LOG" ;;
  "inspect "*)
    printf 'inspect %s\n' "$*" >> "$CALL_LOG"
    printf '4242\n' ;;
  "kill "*)
    printf 'kill %s\n' "$*" >> "$CALL_LOG" ;;
  "logs "*)
    printf 'fake log line\n' >> "$CALL_LOG" ;;
  *) printf 'unexpected podman call: %s\n' "$*" >&2; exit 90 ;;
esac
EOF
cat > "$STUB_BIN/ss" <<'EOF'
#!/usr/bin/env bash
printf 'LISTEN 0 0 127.0.0.1:4802 *:* users:(("rootlessport",pid=111,fd=1))\n'
EOF
cat > "$STUB_BIN/ps" <<'EOF'
#!/usr/bin/env bash
printf '    PID    PPID     ELAPSED CMD\n    111       1       01:00 rootlessport\n'
EOF
cat > "$STUB_BIN/kill" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$KILL_LOG"
EOF
chmod 755 "$STUB_BIN/curl" "$STUB_BIN/podman" "$STUB_BIN/ss" "$STUB_BIN/ps" "$STUB_BIN/kill"
export PATH="$STUB_BIN:$PATH"
export PORT_PROBE_STATE_DIR="$SANDBOX/state"
export MULTICA_MAIN_LOCK_FILE="$SANDBOX/deploy.lock"
export RESTART_LOG="$SANDBOX/restarts.log"
export KILL_LOG="$SANDBOX/kills.log"
export CALL_LOG="$SANDBOX/calls.log"
: > "$RESTART_LOG"
: > "$KILL_LOG"
: > "$CALL_LOG"

run() { bash "$SCRIPT" "$STACK_DIR" "$@"; }
export HOST_BACKEND_OK=1 HOST_FRONTEND_OK=1 CONT_OK=1

# Both sides healthy: exit 0, no restart, no evidence lines.
out="$(run)"
[[ "$out" == *"all sides healthy"* ]]
test ! -s "$RESTART_LOG"

# Forwarder dead on the backend only: the stale forwarder pid is killed, the
# container restarts once, exit 0 after the post-restart host check passes.
export HOST_BACKEND_OK=0
out="$(run)"
[[ "$out" == *"FORWARDER DEAD"* ]]
[[ "$out" == *"killing stale forwarder pid 111 on :4802"* ]]
[[ "$out" == *"healed (host :4802 answers after restart)"* ]]
grep -qx 'cid-backend' "$RESTART_LOG"
grep -qx -- '-9 111' "$KILL_LOG"
test "$(wc -l < "$RESTART_LOG")" -eq 1
grep -q 'forwarder-dead host-port=4802' "$PORT_PROBE_STATE_DIR/probe.log"
export HOST_BACKEND_OK=1
: > "$RESTART_LOG"
: > "$KILL_LOG"

# Container unhealthy once or twice: counted, reported, never restarted.
# The countdown fails backend execs only, so the frontend stays healthy.
rm -rf "$PORT_PROBE_STATE_DIR"
export CONT_FAIL_FILE="$SANDBOX/cont-fails"
printf '3' > "$CONT_FAIL_FILE"
rc=0; out="$(run 2>&1)" || rc=$?
[ "$rc" -ne 0 ]
[[ "$out" == *"CONTAINER UNHEALTHY"*1/3* ]]
test ! -s "$RESTART_LOG"
rc=0; out="$(run 2>&1)" || rc=$?
[ "$rc" -ne 0 ]
[[ "$out" == *"CONTAINER UNHEALTHY"*2/3* ]]
test ! -s "$RESTART_LOG"

# Third consecutive unhealthy tick: capture diagnostics (top, pid, logs and
# SIGQUIT for the backend), restart, and verify the revive. The countdown is
# exhausted, so the post-restart check answers again.
: > "$CALL_LOG"
out="$(run 2>&1)"
[[ "$out" == *"CONTAINER UNHEALTHY"*3/3* ]]
[[ "$out" == *"podman top"* ]]
[[ "$out" == *"sending SIGQUIT for the goroutine dump"* ]]
[[ "$out" == *"revived (in-container check answers after restart)"* ]]
grep -q 'kill -s QUIT cid-backend' "$CALL_LOG"
grep -q '^cid-backend$' "$RESTART_LOG"
grep -q 'container-unhealthy host-port=4802' "$PORT_PROBE_STATE_DIR/probe.log"
# The restart clears the streak: the next tick is healthy again.
out="$(run 2>&1)"
[[ "$out" == *"all sides healthy"* ]]
unset CONT_FAIL_FILE
export CONT_OK=1

# Rate limit: two restarts pass, the third tick in the window refuses.
export STUCK=1
rm -rf "$PORT_PROBE_STATE_DIR"
: > "$RESTART_LOG"
run >/dev/null || true
run >/dev/null || true
test "$(grep -c '^cid-backend$' "$RESTART_LOG")" -eq 2
rc=0; out="$(run 2>&1)" || rc=$?
[ "$rc" -ne 0 ]
[[ "$out" == *"rate limit reached"* ]]
test "$(grep -c '^cid-backend$' "$RESTART_LOG")" -eq 2
unset STUCK

# Lock held by a deploy: the tick skips quietly, exit 0, no checks.
(
  exec 201>"$MULTICA_MAIN_LOCK_FILE"
  flock 201
  sleep 3
) &
locker=$!
sleep 0.5
: > "$RESTART_LOG"
out="$(run)"
[[ "$out" == *"deploy lock held; skipping"* ]]
test ! -s "$RESTART_LOG"
wait "$locker"

# --check mode reports without restarting.
export HOST_FRONTEND_OK=0
rc=0; out="$(bash "$SCRIPT" --check "$STACK_DIR" 2>&1)" || rc=$?
[ "$rc" -ne 0 ]
[[ "$out" == *"FORWARDER DEAD"* ]]
test ! -s "$RESTART_LOG"
export HOST_FRONTEND_OK=1

printf 'stack-port-probe tests passed\n'
