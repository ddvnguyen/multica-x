#!/usr/bin/env bash
# Regression checks for M-301: lane-z autodeploy must refresh
# multica-daemon-z.service after a successful backend switch without
# interrupting an active task. Coordinates with M-232 (pruned-binary recovery)
# by only restarting when idle; busy daemons are deferred, never interrupted.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$(mktemp -d "$REPO_ROOT/.autodeploy-daemon-refresh-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT

export LANE_ROOT="$SANDBOX/lane"
export LANE_HOME="$SANDBOX/home"
export MULTICA_Z_PROFILE=z
export MULTICA_DAEMON_Z_UNIT=multica-daemon-z.service
mkdir -p "$LANE_ROOT/multica-be/current/bin" "$LANE_ROOT/data" "$LANE_HOME"

TARGET_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
OTHER_SHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

calls="$SANDBOX/calls"
state="$SANDBOX/health.json"
: > "$calls"

json_state() { # $1=status $2=cli_commit $3=active_task_count
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
print(json.dumps({"status": sys.argv[1], "cli_commit": sys.argv[2], "active_task_count": int(sys.argv[3])}))
PY
}

cat > "$LANE_ROOT/multica-be/current/bin/multica" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cat "${FAKE_HEALTH_STATE:?}"
EOF
chmod +x "$LANE_ROOT/multica-be/current/bin/multica"

BIN_DIR="$SANDBOX/bin"
mkdir -p "$BIN_DIR"
cat > "$BIN_DIR/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'systemctl %s\n' "$*" >> "${FAKE_CALLS:?}"
# Simulate a successful restart: the daemon comes back running the target.
if [[ "$*" == "--user restart multica-daemon-z.service" ]]; then
  printf '%s\n' "$FAKE_HEALTH_AFTER_RESTART" > "${FAKE_HEALTH_STATE:?}"
fi
EOF
chmod +x "$BIN_DIR/systemctl"

# Stub jq for the helper's simple health-field queries (jq may not be present).
cat > "$BIN_DIR/jq" <<'EOF'
#!/usr/bin/env python3
import json, sys
raw = sys.stdin.read()
try:
    obj = json.loads(raw)
except Exception:
    obj = {}
args = sys.argv[1:]
expr = args[1] if len(args) > 1 and args[0] == "-r" else (args[0] if args else "")
def out(v):
    if v is None:
        print("")
    elif isinstance(v, bool):
        print("true" if v else "false")
    else:
        print(v)
if expr in (".status // empty", ".status//empty"):
    out(obj.get("status") or "")
elif expr in (".cli_commit // empty", ".cli_commit//empty"):
    out(obj.get("cli_commit") or "")
elif expr in ('.cli_commit // "unknown"', ".cli_commit // unknown"):
    out(obj.get("cli_commit") or "unknown")
elif "active_task_count" in expr:
    try:
        out(int(obj.get("active_task_count", 0) or 0))
    except Exception:
        out(0)
else:
    out("")
EOF
chmod +x "$BIN_DIR/jq"

export PATH="$BIN_DIR:$PATH"
export FAKE_CALLS="$calls"
export FAKE_HEALTH_STATE="$state"

log() { printf '[test] %s\n' "$*"; }
# shellcheck source=./lane-lib.sh
. "$REPO_ROOT/scripts/lane-z/lane-lib.sh"

assert_no_restart() {
  if grep -q 'systemctl --user restart multica-daemon-z.service' "$calls"; then
    echo "expected no daemon restart, got:" >&2
    cat "$calls" >&2
    exit 1
  fi
}

assert_restarted() {
  if ! grep -q 'systemctl --user restart multica-daemon-z.service' "$calls"; then
    echo "expected daemon restart, got:" >&2
    cat "$calls" >&2
    exit 1
  fi
}

# Case 1: daemon already running the deployed release -> no restart, pending cleared.
: > "$calls"
printf '%s\n' "$TARGET_SHA" > "$(daemon_refresh_pending_file)"
json_state running "${TARGET_SHA:0:9}" 0 > "$state"
export FAKE_HEALTH_AFTER_RESTART="$(cat "$state")"
refresh_lane_daemon_after_deploy "$TARGET_SHA"
assert_no_restart
test ! -f "$(daemon_refresh_pending_file)"

# Case 2: idle stale daemon -> restart and verify deployed release, pending cleared.
: > "$calls"
rm -f "$(daemon_refresh_pending_file)"
json_state running "${OTHER_SHA:0:9}" 0 > "$state"
export FAKE_HEALTH_AFTER_RESTART="$(json_state running "${TARGET_SHA:0:9}" 0)"
refresh_lane_daemon_after_deploy "$TARGET_SHA"
assert_restarted
test ! -f "$(daemon_refresh_pending_file)"

# Case 3: busy stale daemon -> NO restart (no interruption), pending recorded.
: > "$calls"
rm -f "$(daemon_refresh_pending_file)"
json_state running "${OTHER_SHA:0:9}" 1 > "$state"
export FAKE_HEALTH_AFTER_RESTART="$(json_state running "${TARGET_SHA:0:9}" 0)"
if refresh_lane_daemon_after_deploy "$TARGET_SHA"; then
  :
else
  echo "busy deferral must not fail the refresh helper" >&2
  exit 1
fi
assert_no_restart
test -f "$(daemon_refresh_pending_file)"
grep -qx "$TARGET_SHA" "$(daemon_refresh_pending_file)"

# Case 4: busy then idle on retry -> restart completes (deferred refresh path).
: > "$calls"
# state shows busy/stale from case 3; simulate task finished before retry
json_state running "${OTHER_SHA:0:9}" 0 > "$state"
export FAKE_HEALTH_AFTER_RESTART="$(json_state running "${TARGET_SHA:0:9}" 0)"
refresh_lane_daemon_after_deploy "$TARGET_SHA"
assert_restarted
test ! -f "$(daemon_refresh_pending_file)"

printf 'autodeploy daemon refresh tests passed\n'
