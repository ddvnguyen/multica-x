#!/usr/bin/env bash
# Regression checks for M-455: lane-z rollback PWA fallback to previous-release.
#
# When rollback runs without --to and mmx-pwa/releases/<backend-target-SHA>
# is absent (first post-merge deploy gap), it falls back to the release named
# in mmx-pwa/previous-release when that names a complete release: switch,
# rewrite env for the fallback SHA, restart, health-gate it.
#   * default path with absent same-SHA PWA dir converges to the marker release
#   * explicit --to keeps exact SHA pairing (no fallback, loud warning)
#   * explicit --to with the same-SHA PWA release present pairs to it exactly
#   * missing PWA release AND no usable marker stays a loud warning, exit 0
#   * a marker naming an unstamped pre-M-451 tree is unusable: warn, no switch
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$REPO_ROOT/.rollback-mmx-pwa-test.$$"
trap 'rm -rf "$SANDBOX"' EXIT
mkdir -p "$SANDBOX/bin" "$SANDBOX/mock-bin" "$SANDBOX/home"

cp "$REPO_ROOT/scripts/lane-z/rollback.sh" "$REPO_ROOT/scripts/lane-z/lane-lib.sh" "$SANDBOX/bin/"
cat > "$SANDBOX/bin/dsh-z.env" <<'EOF'
LANE_ROOT="$TEST_LANE_ROOT"
LANE_HOME="$TEST_LANE_ROOT/home"
LANE_RUNTIME_ENV="$TEST_LANE_ROOT/runtime.env"
MULTICA_BE_Z_PORT=4390
MULTICA_BE_Z_UNIT=multica-be-z.service
MMX_PWA_Z_PORT=3190
MMX_PWA_Z_UNIT=mmx-pwa-z.service
MULTICA_Z_DB_NAME=multica_z
MULTICA_Z_DB_CONTAINER=multica-z-db
EOF

# systemctl: all restarts/reloads succeed; record calls for assertions.
cat > "$SANDBOX/mock-bin/systemctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_LANE_ROOT/systemctl.log"
exit 0
EOF
chmod +x "$SANDBOX/mock-bin/systemctl"

# curl: backend health from the CURRENT backend release.json; PWA health from
# the CURRENT PWA tree so post-switch gates pass when the tree is correct.
# The commit check is matched by path and logs the full URL: it must hit the
# lane-web host resolved from the unit (M-458), which a fixed host assertion
# would bake in instead of checking.
cat > "$SANDBOX/mock-bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
case "$url" in
  http://127.0.0.1:4390/healthz)
    printf '{"status":"ok"}\n' ;;
  */multica/health)
    printf '%s\n' "$url" >> "$TEST_LANE_ROOT/curl.log"
    sha="$(jq -r '.sha // empty' "$LANE_ROOT/multica-be/current/release.json" 2>/dev/null || true)"
    printf '{"commit":"%s"}\n' "$sha" ;;
  http://127.0.0.1:3190/manifest.webmanifest)
    printf '{"id":"/?mmx-install","name":"MMX","short_name":"MMX"}\n' ;;
  http://127.0.0.1:3190/sw.js)
    cat "$LANE_ROOT/mmx-pwa/current/apps/web/public/sw.js" ;;
  *) printf 'unexpected curl url: %s\n' "$url" >&2; exit 2 ;;
esac
EOF
chmod +x "$SANDBOX/mock-bin/curl"
export PATH="$SANDBOX/mock-bin:$PATH"

TARGET_SHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
NEW_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
FALLBACK_SHA="cccccccccccccccccccccccccccccccccccccccc"
export TARGET_SHA NEW_SHA FALLBACK_SHA

new_lane() { # $1 = variant: fallback-ok | explicit | missing-everything | stale-marker |
             #              explicit-pair | unstamped-marker
  local variant="$1"
  TEST_LANE_ROOT="$SANDBOX/lane-$variant-$$"
  export TEST_LANE_ROOT
  rm -rf "$TEST_LANE_ROOT"
  mkdir -p "$TEST_LANE_ROOT/multica-be/releases" "$TEST_LANE_ROOT/mmx-pwa/releases" \
    "$TEST_LANE_ROOT/data" "$TEST_LANE_ROOT/home"
  # Backend: current is NEW (broken), previous points at TARGET.
  mkdir -p "$TEST_LANE_ROOT/multica-be/releases/$NEW_SHA" "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
  for s in "$NEW_SHA" "$TARGET_SHA"; do
    mkdir -p "$TEST_LANE_ROOT/multica-be/releases/$s/bin"
    printf '#!/bin/sh\nexit 0\n' > "$TEST_LANE_ROOT/multica-be/releases/$s/bin/server"
    chmod +x "$TEST_LANE_ROOT/multica-be/releases/$s/bin/server"
    printf '{"sha":"%s","version":"test"}\n' "$s" > "$TEST_LANE_ROOT/multica-be/releases/$s/release.json"
    : > "$TEST_LANE_ROOT/multica-be/releases/$s/.complete"
  done
  ln -s "$TEST_LANE_ROOT/multica-be/releases/$NEW_SHA" "$TEST_LANE_ROOT/multica-be/current"
  printf '%s\n' "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA" > "$TEST_LANE_ROOT/multica-be/previous-release"
  # PWA new tree (pairs with NEW_SHA, currently live).
  mkdir -p "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/apps/web/.next"
  printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/apps/web/server.js"
  printf 'const VERSION = "%s";\n' "$NEW_SHA" > "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/apps/web/public/sw.js"
  printf 'mmx-install\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/apps/web/.next/brand"
  : > "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA/.complete"
  ln -s "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA" "$TEST_LANE_ROOT/mmx-pwa/current"
  printf 'MULTICA_Z_DEV_CODE=123456\n' > "$TEST_LANE_ROOT/runtime.env"
  export HOME="$SANDBOX/home" LANE_ROOT="$TEST_LANE_ROOT" LANE_HOME="$TEST_LANE_ROOT/home"
  export LANE_RUNTIME_ENV="$TEST_LANE_ROOT/runtime.env"
  export MULTICA_BE_Z_PORT=4390 MULTICA_BE_Z_UNIT=multica-be-z.service
  export MMX_PWA_Z_PORT=3190 MMX_PWA_Z_UNIT=mmx-pwa-z.service
  export MMX_AUTH_BYPASS=1
  rm -f "$TEST_LANE_ROOT/systemctl.log"
  case "$variant" in
    fallback-ok)
      mkdir -p "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next"
      printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/server.js"
      printf 'const VERSION = "%s";\n' "$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public/sw.js"
      printf 'mmx-install\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next/brand"
      : > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/.complete"
      printf '%s\n' "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
    explicit)
      mkdir -p "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next"
      printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/server.js"
      printf 'const VERSION = "%s";\n' "$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public/sw.js"
      printf 'mmx-install\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next/brand"
      : > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/.complete"
      printf '%s\n' "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
    missing-everything)
      rm -f "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
    stale-marker)
      printf '%s\n' "$TEST_LANE_ROOT/mmx-pwa/releases/deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" > "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
    explicit-pair)
      mkdir -p "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/apps/web/.next"
      printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/apps/web/server.js"
      printf 'const VERSION = "%s";\n' "$TARGET_SHA" > "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/apps/web/public/sw.js"
      printf 'mmx-install\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/apps/web/.next/brand"
      : > "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA/.complete"
      printf '%s\n' "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
    unstamped-marker)
      mkdir -p "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next"
      printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/server.js"
      printf 'const VERSION = "__MMX_PWA_VERSION__";\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/public/sw.js"
      printf 'mmx-install\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/apps/web/.next/brand"
      : > "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA/.complete"
      printf '%s\n' "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA" > "$TEST_LANE_ROOT/mmx-pwa/previous-release"
      ;;
  esac
}

# Case 1: default rollback converges the PWA to the marker release.
new_lane fallback-ok
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "falling back to previous-release"
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$FALLBACK_SHA"
grep -qx "NEXT_PUBLIC_APP_VERSION=$FALLBACK_SHA" "$TEST_LANE_ROOT/mmx-pwa/env"
echo "$out" | grep -q "mmx-pwa z is on ${FALLBACK_SHA:0:9}"
grep -q "restart mmx-pwa-z.service" "$TEST_LANE_ROOT/systemctl.log"
# No lane-web unit is configured here, so the 127.0.0.1:3180 fallback is used.
grep -qx 'http://127.0.0.1:3180/multica/health' "$TEST_LANE_ROOT/curl.log"

# Case 2: explicit --to keeps exact pairing (no fallback).
new_lane explicit
out="$("$SANDBOX/bin/rollback.sh" --to "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA" 2>&1)"
echo "$out" | grep -q "WARNING: no mmx-pwa release for ${TARGET_SHA:0:9}"
if echo "$out" | grep -q "falling back"; then echo "explicit --to must not fall back" >&2; exit 1; fi
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA"

# Case 3: missing PWA release and no marker warns, exit 0, backend still rolls back.
new_lane missing-everything
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "WARNING: no mmx-pwa release for ${TARGET_SHA:0:9} on disk and no usable mmx-pwa/previous-release marker"
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA"

# Case 4: stale marker (points at absent release) also warns, exit 0.
new_lane stale-marker
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "no usable mmx-pwa/previous-release marker"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA"

# Case 5: explicit --to with a same-SHA PWA release present pairs exactly to it,
# never to the different release the marker names.
new_lane explicit-pair
out="$("$SANDBOX/bin/rollback.sh" --to "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA" 2>&1)"
if echo "$out" | grep -q "falling back"; then echo "explicit --to must not fall back" >&2; exit 1; fi
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA"
grep -qx "NEXT_PUBLIC_APP_VERSION=$TARGET_SHA" "$TEST_LANE_ROOT/mmx-pwa/env"
echo "$out" | grep -q "mmx-pwa z is on ${TARGET_SHA:0:9}"

# Case 6: a marker naming an unstamped pre-M-451 tree is unusable — warn, leave
# the PWA where it is, exit 0 (the backend is already healthy).
new_lane unstamped-marker
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "whose sw.js is not stamped with that SHA"
echo "$out" | grep -q "no usable mmx-pwa/previous-release marker"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$NEW_SHA"

# Case 7 (M-458): the lane web binds the podman bridge, not loopback. With
# dsh-z.service's ExecStart naming 10.88.0.1:3180 the commit check must hit that
# host; the old hardcoded 127.0.0.1:3180 could never pass there, so every
# rollback printed ROLLBACK FAILED over a correctly switched backend.
new_lane fallback-ok
export DSH_Z_WEB_UNIT=dsh-z.service
mkdir -p "$HOME/.config/systemd/user"
printf 'ExecStart=/usr/bin/dsh web --host 10.88.0.1 --port 3180\n' > "$HOME/.config/systemd/user/dsh-z.service"
out="$("$SANDBOX/bin/rollback.sh" 2>&1)"
echo "$out" | grep -q "lane z backend is on ${TARGET_SHA:0:9}"
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
grep -qx 'http://10.88.0.1:3180/multica/health' "$TEST_LANE_ROOT/curl.log"
! grep -q '127.0.0.1:3180' "$TEST_LANE_ROOT/curl.log"
rm -f "$HOME/.config/systemd/user/dsh-z.service"

printf 'rollback mmx-pwa fallback tests passed\n'
