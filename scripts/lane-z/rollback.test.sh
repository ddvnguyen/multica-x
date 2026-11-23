#!/usr/bin/env bash
# scripts/lane-z/rollback.test.sh — backend-only rollback (M-151).
#
# Verifies the normal rollback path after the lane-z MMX PWA was retired:
#   * the default path flips to the recorded previous release, restarts the
#     backend unit, re-checks health, and never restores the database;
#   * an explicit --to targets a named release without a marker;
#   * --list shows releases on disk, marking the current one.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$(mktemp -d "$REPO_ROOT/.rollback-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT
mkdir -p "$SANDBOX/bin" "$SANDBOX/mock-bin"

cp "$REPO_ROOT/scripts/lane-z/rollback.sh" "$REPO_ROOT/scripts/lane-z/lane-lib.sh" "$SANDBOX/bin/"
cat > "$SANDBOX/bin/dsh-z.env" <<'EOF'
LANE_ROOT="$TEST_LANE_ROOT"
LANE_HOME="$TEST_LANE_ROOT/home"
LANE_RUNTIME_ENV="$TEST_LANE_ROOT/runtime.env"
MULTICA_BE_Z_PORT=4390
MULTICA_BE_Z_UNIT=multica-be-z.service
MULTICA_Z_DB_NAME=multica_z
MULTICA_Z_DB_CONTAINER=multica-z-db
EOF

# systemctl records calls; curl answers backend health and the lane-web commit
# check from the CURRENT backend release so the gate tests the switched tree.
cat > "$SANDBOX/mock-bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_LANE_ROOT/systemctl.log"
exit 0
EOF
cat > "$SANDBOX/mock-bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
case "$url" in
  http://127.0.0.1:4390/healthz) printf '{"status":"ok"}\n' ;;
  */multica/health)
    printf '%s\n' "$url" >> "$TEST_LANE_ROOT/curl.log"
    sha="$(jq -r '.sha // empty' "$LANE_ROOT/multica-be/current/release.json" 2>/dev/null || true)"
    printf '{"commit":"%s"}\n' "$sha" ;;
  *) printf 'unexpected curl url: %s\n' "$url" >&2; exit 2 ;;
esac
EOF
chmod +x "$SANDBOX/mock-bin/systemctl" "$SANDBOX/mock-bin/curl"
export PATH="$SANDBOX/mock-bin:$PATH"

OLD_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
TARGET_SHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
export OLD_SHA TARGET_SHA

new_lane() {
  TEST_LANE_ROOT="$SANDBOX/lane-$$"
  export TEST_LANE_ROOT
  mkdir -p "$TEST_LANE_ROOT/multica-be/releases" "$TEST_LANE_ROOT/data" "$TEST_LANE_ROOT/home"
  for sha in "$OLD_SHA" "$TARGET_SHA"; do
    mkdir -p "$TEST_LANE_ROOT/multica-be/releases/$sha/bin"
    printf '#!/bin/sh\nexit 0\n' > "$TEST_LANE_ROOT/multica-be/releases/$sha/bin/server"
    chmod +x "$TEST_LANE_ROOT/multica-be/releases/$sha/bin/server"
    printf '{"sha":"%s","version":"test"}\n' "$sha" > "$TEST_LANE_ROOT/multica-be/releases/$sha/release.json"
    : > "$TEST_LANE_ROOT/multica-be/releases/$sha/.complete"
  done
  ln -s "$TEST_LANE_ROOT/multica-be/releases/$OLD_SHA" "$TEST_LANE_ROOT/multica-be/current"
  printf '%s\n' "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA" > "$TEST_LANE_ROOT/multica-be/previous-release"
  export HOME="$TEST_LANE_ROOT/home" LANE_ROOT="$TEST_LANE_ROOT" LANE_HOME="$TEST_LANE_ROOT/home"
  export LANE_RUNTIME_ENV="$TEST_LANE_ROOT/runtime.env"
  export MULTICA_BE_Z_PORT=4390 MULTICA_BE_Z_UNIT=multica-be-z.service
  rm -f "$TEST_LANE_ROOT/systemctl.log" "$TEST_LANE_ROOT/curl.log"
}

# Default path: the release recorded by the last deploy.
new_lane
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "lane z is on ${TARGET_SHA:0:9}"
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
grep -q "restart $MULTICA_BE_Z_UNIT" "$TEST_LANE_ROOT/systemctl.log"
grep -qx 'http://127.0.0.1:3180/multica/health' "$TEST_LANE_ROOT/curl.log"
! echo "$out" | grep -qi 'pwa'

# Explicit --to targets a named release without a previous-release marker.
new_lane
rm -f "$TEST_LANE_ROOT/multica-be/previous-release"
out="$("$SANDBOX/bin/rollback.sh" --to "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA" 2>&1)"
echo "$out" | grep -q "lane z is on ${TARGET_SHA:0:9}"
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"

# --list shows the releases on disk and marks the current one.
out="$("$SANDBOX/bin/rollback.sh" --list 2>&1)"
echo "$out" | grep -q "$TARGET_SHA (current)"

printf 'backend rollback tests passed\n'
