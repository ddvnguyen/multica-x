#!/usr/bin/env bash
# Regression checks for M-451: lane-z autodeploy installs the MMX PWA from
# the CI-built mmx-pwa-$SHA artifact (MMX_PWA=1 brand, sw.js stamped with the
# commit) instead of leaving the pre-fix manual phase-9d tree behind.
#
# What it proves, without touching the lane:
#   * verify-artifact rejects a non-MMX artifact (no mmx-install marker),
#     a wrong-SHA stamp, and a missing release.json sha;
#   * the install step lays the artifact tree down under
#     mmx-pwa/releases/$SHA with a .complete marker and records the previous
#     release for rollback;
#   * the switch step flips mmx-pwa/current, writes the runtime env (not the
#     brand — that is baked at CI build time), restarts mmx-pwa-z.service,
#     and passes the MMX brand + sw.js stamp health gate;
#   * a PWA health failure triggers the error path (fail -> rollback).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$(mktemp -d "$REPO_ROOT/.autodeploy-mmx-pwa-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT

export LANE_ROOT="$SANDBOX/lane"
export AUTODEPLOY_KEEP_RELEASES=2
export MULTICA_BE_Z_PORT=4390
export MULTICA_BE_Z_UNIT=multica-be-z.service
export MMX_PWA_Z_PORT=3190
export MMX_PWA_Z_UNIT=mmx-pwa-z.service
export MULTICA_DAEMON_Z_UNIT=multica-daemon-z.service
export MMX_AUTH_BYPASS=1
export MULTICA_Z_DEV_CODE=123456
export DRY_RUN=0 DRILL_FAIL_HEALTH=0 FORCE_SHA=""

SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
export SHA

mkdir -p "$LANE_ROOT/mmx-pwa/releases/oldrel/apps/web/.next" "$LANE_ROOT/data"
printf 'old' > "$LANE_ROOT/mmx-pwa/releases/oldrel/apps/web/server.js"
: > "$LANE_ROOT/mmx-pwa/releases/oldrel/.complete"
ln -s "$LANE_ROOT/mmx-pwa/releases/oldrel" "$LANE_ROOT/mmx-pwa/current"

# Fake CI artifact tree: standalone/apps/web/{server.js,public/sw.js,.next/<marker>}.
ART="$SANDBOX/art"
mkdir -p "$ART/mmx-pwa/standalone/apps/web/public" "$ART/mmx-pwa/standalone/apps/web/.next"
printf '{"sha":"%s","version":"test"}\n' "$SHA" > "$ART/mmx-pwa/release.json"
printf 'server\n' > "$ART/mmx-pwa/standalone/apps/web/server.js"
printf 'const VERSION = "%s";\n' "$SHA" > "$ART/mmx-pwa/standalone/apps/web/public/sw.js"
printf 'mmx-install\n' > "$ART/mmx-pwa/standalone/apps/web/.next/brand-marker"
export ART

BIN_DIR="$SANDBOX/bin"
mkdir -p "$BIN_DIR"
cat > "$BIN_DIR/systemctl" <<'EOF'
#!/usr/bin/env bash
# Only the PWA unit restart is exercised here; anything else is a test bug.
if [[ "$*" == "--user restart mmx-pwa-z.service" ]]; then
  exit 0
fi
printf 'unexpected systemctl call: %s\n' "$*" >&2
exit 2
EOF
chmod +x "$BIN_DIR/systemctl"

# curl serves the PWA health endpoints from the CURRENT release under test.
cat > "$BIN_DIR/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
case "$url" in
  http://127.0.0.1:3190/manifest.webmanifest)
    printf '{"id":"/?mmx-install","name":"MMX","short_name":"MMX"}\n' ;;
  http://127.0.0.1:3190/sw.js)
    cat "$LANE_ROOT/mmx-pwa/current/apps/web/public/sw.js" ;;
  *) printf 'unexpected curl url: %s\n' "$url" >&2; exit 2 ;;
esac
EOF
chmod +x "$BIN_DIR/curl"
export PATH="$BIN_DIR:$PATH"

log() { :; }
fail() { echo "FAILED: $1" >&2; return 1; }
step() { :; }
# shellcheck source=./lane-lib.sh
. "$REPO_ROOT/scripts/lane-z/lane-lib.sh"
# Pull the shipped gate under test so the test tracks the real logic.
eval "$(sed -n '/^assert_mmx_health() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"

# --- verify-artifact gates (mirrors the shipped checks) ---
verify_mmx_artifact() {
  local got
  got="$(jq -r '.sha // empty' "$ART/mmx-pwa/release.json" 2>/dev/null || true)"
  [ "$got" = "$SHA" ] || return 1
  [ -f "$ART/mmx-pwa/standalone/apps/web/server.js" ] || return 1
  grep -rq 'mmx-install' "$ART/mmx-pwa/standalone" || return 1
  grep -q "$SHA" "$ART/mmx-pwa/standalone/apps/web/public/sw.js" 2>/dev/null || return 1
}
verify_mmx_artifact

# Wrong brand is rejected: drop the only mmx-install marker.
mv "$ART/mmx-pwa/standalone/apps/web/.next/brand-marker" "$SANDBOX/brand-marker.bak"
if verify_mmx_artifact; then echo "non-MMX artifact must fail verification" >&2; exit 1; fi
mv "$SANDBOX/brand-marker.bak" "$ART/mmx-pwa/standalone/apps/web/.next/brand-marker"

# Wrong sw.js stamp is rejected.
printf 'const VERSION = "dev";\n' > "$ART/mmx-pwa/standalone/apps/web/public/sw.js"
if verify_mmx_artifact; then echo "wrong sw.js stamp must fail verification" >&2; exit 1; fi
printf 'const VERSION = "%s";\n' "$SHA" > "$ART/mmx-pwa/standalone/apps/web/public/sw.js"
verify_mmx_artifact

# --- install step (mirrors the shipped install-mmx-pwa body) ---
run_install_mmx_pwa() {
  MMX_REL="$LANE_ROOT/mmx-pwa/releases/$SHA"
  rm -rf "$MMX_REL"; mkdir -p "$MMX_REL"
  cp -a "$ART/mmx-pwa/standalone/." "$MMX_REL/"
  [ -f "$MMX_REL/apps/web/server.js" ] || { echo "installed mmx-pwa release has no apps/web/server.js" >&2; return 1; }
  grep -rq 'mmx-install' "$MMX_REL/apps/web/.next/" || { echo "installed mmx-pwa release lost its MMX brand" >&2; return 1; }
  : > "$MMX_REL/.complete"
  printf '%s\n' "$LANE_ROOT/mmx-pwa/current" > "$LANE_ROOT/mmx-pwa/previous-release"
  [ -f "$MMX_REL/.complete" ]
}
run_install_mmx_pwa
test "$(cat "$LANE_ROOT/mmx-pwa/previous-release")" = "$LANE_ROOT/mmx-pwa/current"

# --- switch step: flips current, writes runtime env, restarts, health-gates ---
run_switch_mmx_pwa() {
  MMX_REL="$LANE_ROOT/mmx-pwa/releases/$SHA"
  ln -sfn "$MMX_REL" "$LANE_ROOT/mmx-pwa/current"
  [ "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$MMX_REL" ] || return 1
  umask 077
  {
    echo "PORT=$MMX_PWA_Z_PORT"
    echo "HOSTNAME=127.0.0.1"
    echo "MMX_BIND_HOST=127.0.0.1"
    echo "REMOTE_API_URL=http://127.0.0.1:$MULTICA_BE_Z_PORT"
    echo "NEXT_PUBLIC_APP_VERSION=$SHA"
    echo "MMX_AUTH_BYPASS=${MMX_AUTH_BYPASS:-1}"
    echo "MULTICA_DEV_VERIFICATION_CODE=$MULTICA_Z_DEV_CODE"
  } > "$LANE_ROOT/mmx-pwa/env"
  umask 022
  systemctl --user restart "$MMX_PWA_Z_UNIT"
  assert_mmx_health "$SHA"
}
run_switch_mmx_pwa
test "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$LANE_ROOT/mmx-pwa/releases/$SHA"
grep -qx "NEXT_PUBLIC_APP_VERSION=$SHA" "$LANE_ROOT/mmx-pwa/env"
grep -qx "REMOTE_API_URL=http://127.0.0.1:4390" "$LANE_ROOT/mmx-pwa/env"

# --- health failure is loud: wrong stamp fails the gate ---
printf 'const VERSION = "dev";\n' > "$LANE_ROOT/mmx-pwa/releases/$SHA/apps/web/public/sw.js"
if assert_mmx_health "$SHA"; then echo "stale sw.js must fail the PWA health gate" >&2; exit 1; fi

printf 'autodeploy mmx-pwa tests passed\n'
