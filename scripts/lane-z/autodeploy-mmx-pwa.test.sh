#!/usr/bin/env bash
# Regression checks for M-451: lane-z autodeploy installs the MMX PWA from
# the CI-built mmx-pwa-$SHA artifact (MMX_PWA=1 brand, sw.js stamped with the
# commit) instead of leaving the pre-fix manual phase-9d tree behind.
#
# What it proves, without touching the lane:
#   * verify-artifact rejects a non-MMX artifact (default manifest body),
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
# Failure-injection flags default off: an ambient FAIL_SW/FAIL_RESTART in the
# calling shell must not change what the mocks serve.
export FAIL_SW=0 FAIL_RESTART=0

SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
export SHA

mkdir -p "$LANE_ROOT/mmx-pwa/releases/oldrel/apps/web/.next" "$LANE_ROOT/data"
printf 'old' > "$LANE_ROOT/mmx-pwa/releases/oldrel/apps/web/server.js"
: > "$LANE_ROOT/mmx-pwa/releases/oldrel/.complete"
ln -s "$LANE_ROOT/mmx-pwa/releases/oldrel" "$LANE_ROOT/mmx-pwa/current"

# Fake CI artifact tree: standalone/apps/web/{server.js,public/sw.js,
# .next/server/app/manifest.webmanifest.body}. The server chunk embeds BOTH
# brands' literals (as real builds do), proving the gate reads the manifest
# route output and not a raw string.
ART="$SANDBOX/art"
mkdir -p "$ART/mmx-pwa/standalone/apps/web/public" "$ART/mmx-pwa/standalone/apps/web/.next/server/app" "$ART/mmx-pwa/standalone/apps/web/.next/server/chunks"
printf '{"sha":"%s","version":"test"}\n' "$SHA" > "$ART/mmx-pwa/release.json"
printf 'server\n' > "$ART/mmx-pwa/standalone/apps/web/server.js"
printf 'const VERSION = "%s";\n' "$SHA" > "$ART/mmx-pwa/standalone/apps/web/public/sw.js"
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$ART/mmx-pwa/standalone/apps/web/.next/server/app/manifest.webmanifest.body"
printf '/?mmx-install multica-web both literals\n' > "$ART/mmx-pwa/standalone/apps/web/.next/server/chunks/both.js"
export ART

BIN_DIR="$SANDBOX/bin"
mkdir -p "$BIN_DIR"
cat > "$BIN_DIR/systemctl" <<'EOF'
#!/usr/bin/env bash
# Only the PWA unit restart is exercised here; anything else is a test bug.
# A restart stamps the mock PWA's dead window (M-459): a real restarted Next
# server refuses connections for a moment before it serves.
if [[ "$*" == "--user restart mmx-pwa-z.service" ]]; then
  [ -z "${PWA_RESTART_STAMP:-}" ] || date +%s > "$PWA_RESTART_STAMP"
  exit 0
fi
printf 'unexpected systemctl call: %s\n' "$*" >&2
exit 2
EOF
chmod +x "$BIN_DIR/systemctl"

# curl serves the PWA health endpoints from the CURRENT release under test.
# While $PWA_DOWN_SECONDS have not passed since the unit restart, every
# request is refused with curl's connection-refused exit (7).
cat > "$BIN_DIR/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
if [ -n "${PWA_RESTART_STAMP:-}" ] && [ -f "$PWA_RESTART_STAMP" ] \
  && [ "$(( $(date +%s) - $(cat "$PWA_RESTART_STAMP") ))" -lt "${PWA_DOWN_SECONDS:-0}" ]; then
  printf 'curl: (7) Failed to connect to 127.0.0.1 port 3190\n' >&2
  exit 7
fi
case "$url" in
  http://127.0.0.1:3190/manifest.webmanifest)
    if [ "${FAIL_BRAND:-0}" = 1 ]; then printf '{"id":"/","name":"Multica"}\n'; else printf '{"id":"/?mmx-install","name":"MMX","short_name":"MMX"}\n'; fi ;;
  http://127.0.0.1:3190/sw.js)
    # FAIL_SW=1 simulates a unit that restarted but serves the wrong
    # content — the post-switch health gate must catch it and restore.
    if [ "${FAIL_SW:-0}" = 1 ]; then printf 'const VERSION = "dev";\n'; else cat "$LANE_ROOT/mmx-pwa/current/apps/web/public/sw.js"; fi ;;
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
# Pull the shipped converge/restore/promote/env functions so the failure and
# directory tests track the real production logic.
eval "$(sed -n '/^assert_mmx_health() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"
eval "$(sed -n '/^converge_mmx_pwa() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"
eval "$(sed -n '/^converge_restore_pwa() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"
eval "$(sed -n '/^promote_staged() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"
eval "$(sed -n '/^write_mmx_pwa_env() {/,/^}/p' "$REPO_ROOT/scripts/lane-z/autodeploy.sh")"
export LANE_RUNTIME_ENV="$SANDBOX/runtime.env"
export LANE_HOME="$SANDBOX/home"
printf 'MULTICA_Z_DEV_CODE=123456\n' > "$LANE_RUNTIME_ENV"

# --- verify-artifact gates (mirrors the shipped checks) ---
verify_mmx_artifact() {
  local got
  got="$(jq -r '.sha // empty' "$ART/mmx-pwa/release.json" 2>/dev/null || true)"
  [ "$got" = "$SHA" ] || return 1
  [ -f "$ART/mmx-pwa/standalone/apps/web/server.js" ] || return 1
  grep -q '"id":"/?mmx-install"' "$ART/mmx-pwa/standalone/apps/web/.next/server/app/manifest.webmanifest.body" 2>/dev/null || return 1
  grep -q "$SHA" "$ART/mmx-pwa/standalone/apps/web/public/sw.js" 2>/dev/null || return 1
}
verify_mmx_artifact

# Wrong brand is rejected: the manifest body carries the default id even
# though server chunks still contain both literals (as real builds do).
printf '{"id":"/","name":"Multica"}\n' > "$ART/mmx-pwa/standalone/apps/web/.next/server/app/manifest.webmanifest.body"
if verify_mmx_artifact; then echo "non-MMX artifact must fail verification" >&2; exit 1; fi
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$ART/mmx-pwa/standalone/apps/web/.next/server/app/manifest.webmanifest.body"

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
  grep -q '"id":"/?mmx-install"' "$MMX_REL/apps/web/.next/server/app/manifest.webmanifest.body" 2>/dev/null || { echo "installed mmx-pwa release lost its MMX brand" >&2; return 1; }
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

# --- M-459: the gate rides out the restarted PWA's dead window ---
# A tick restarts mmx-pwa-z and the gate curls it immediately, while a fresh
# server still refuses connections (measured at t=0s down, t=1s serving). The
# pre-restart single-shot gate lost that race on the live lane; the
# post-restart callers' wait must not. Both halves run against the same
# stamped restart below.
export PWA_RESTART_STAMP="$SANDBOX/pwa-restarted.at"
export PWA_DOWN_SECONDS=3
systemctl --user restart "$MMX_PWA_Z_UNIT"
start="$(date +%s)"
if assert_mmx_health "$SHA"; then
  echo "a single-shot gate must fail inside the restarted PWA's dead window" >&2; exit 1
fi
test "$(( $(date +%s) - start ))" -lt 5
assert_mmx_health "$SHA" 60 || { echo "the waiting gate must pass once the PWA answers" >&2; exit 1; }
test "$(( $(date +%s) - start ))" -lt 20

# --- M-459: a PWA that never comes up fails at the wait bound, loudly ---
export PWA_DOWN_SECONDS=600
systemctl --user restart "$MMX_PWA_Z_UNIT"
gate_log=""
log() { gate_log+="$*"$'\n'; }
start="$(date +%s)"
if assert_mmx_health "$SHA" 4; then
  echo "a PWA that never answers must fail the health gate" >&2; exit 1
fi
test "$(( $(date +%s) - start ))" -lt 20
printf '%s' "$gate_log" | grep -q 'manifest did not answer (waited 4s)'
# A live PWA with the wrong brand keeps its own message: the failure names the
# brand, not a dead server.
unset PWA_DOWN_SECONDS PWA_RESTART_STAMP
export FAIL_BRAND=1
gate_log=""
if assert_mmx_health "$SHA"; then echo "a non-MMX manifest must fail the gate" >&2; exit 1; fi
printf '%s' "$gate_log" | grep -q 'manifest is not the MMX brand'
! printf '%s' "$gate_log" | grep -q 'did not answer'
unset FAIL_BRAND

# --- M-459: the pre-switch precheck must not stall while the PWA is down ---
# `already on this SHA?` is the default call shape; a 60s wait there would
# cost a tick a full minute whenever the PWA is down.
export PWA_RESTART_STAMP="$SANDBOX/pwa-restarted.at" PWA_DOWN_SECONDS=600
systemctl --user restart "$MMX_PWA_Z_UNIT"
start="$(date +%s)"
if assert_mmx_health "$SHA" 2>/dev/null; then
  echo "the precheck must report a down PWA as not current" >&2; exit 1
fi
test "$(( $(date +%s) - start ))" -lt 5
unset PWA_DOWN_SECONDS PWA_RESTART_STAMP
log() { :; }

# --- health failure is loud: wrong stamp fails the gate ---
printf 'const VERSION = "dev";\n' > "$LANE_ROOT/mmx-pwa/releases/$SHA/apps/web/public/sw.js"
if assert_mmx_health "$SHA"; then echo "stale sw.js must fail the PWA health gate" >&2; exit 1; fi

# --- promote_staged: never merges into an existing directory ---
# Same-SHA stale tree: promotion replaces it, never nests inside it.
STALE_SHA="cccccccccccccccccccccccccccccccccccccccc"
mkdir -p "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/nested-junk"
printf 'stale\n' > "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/stale-file"
mkdir -p "$SANDBOX/stage-new/apps/web/.next/server/app"
printf 'server\n' > "$SANDBOX/stage-new/apps/web/server.js"
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$SANDBOX/stage-new/apps/web/.next/server/app/manifest.webmanifest.body"
promote_staged "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA" "$SANDBOX/stage-new"
test -f "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/apps/web/server.js"
test ! -e "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/stale-file"
test ! -e "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/nested-junk"
test ! -e "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA/apps/web/apps"
rm -rf "$LANE_ROOT/mmx-pwa/releases/$STALE_SHA"
# Fresh target: plain install works.
mkdir -p "$SANDBOX/stage-fresh/apps/web/.next/server/app"
printf 'server\n' > "$SANDBOX/stage-fresh/apps/web/server.js"
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$SANDBOX/stage-fresh/apps/web/.next/server/app/manifest.webmanifest.body"
promote_staged "$LANE_ROOT/mmx-pwa/releases/freshrel" "$SANDBOX/stage-fresh"
test -f "$LANE_ROOT/mmx-pwa/releases/freshrel/apps/web/server.js"
rm -rf "$LANE_ROOT/mmx-pwa/releases/freshrel"

# --- converge_mmx_pwa: post-switch failures restore the exact prior state ---
# Setup: the PWA currently serves OLD_SHA; the artifact tree carries NEW_SHA.
OLD_SHA="dddddddddddddddddddddddddddddddddddddddd"
NEW_SHA="eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
rm -rf "$LANE_ROOT/mmx-pwa/releases" "$LANE_ROOT/mmx-pwa/current" "$LANE_ROOT/mmx-pwa/previous-release" "$LANE_ROOT/mmx-pwa/env"
mkdir -p "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/apps/web/public" "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/apps/web/.next/server/app"
printf 'server\n' > "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/apps/web/server.js"
printf 'const VERSION = "%s";\n' "$OLD_SHA" > "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/apps/web/public/sw.js"
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/apps/web/.next/server/app/manifest.webmanifest.body"
: > "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA/.complete"
ln -s "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA" "$LANE_ROOT/mmx-pwa/current"
printf 'PRIOR_MARKER_CONTENT\n' > "$LANE_ROOT/mmx-pwa/previous-release"
printf 'PRIOR_ENV_CONTENT\n' > "$LANE_ROOT/mmx-pwa/env"
mkdir -p "$SANDBOX/newart/mmx-pwa/standalone/apps/web/public" "$SANDBOX/newart/mmx-pwa/standalone/apps/web/.next/server/app"
printf '{"sha":"%s"}\n' "$NEW_SHA" > "$SANDBOX/newart/mmx-pwa/release.json"
printf 'server\n' > "$SANDBOX/newart/mmx-pwa/standalone/apps/web/server.js"
printf 'const VERSION = "%s";\n' "$NEW_SHA" > "$SANDBOX/newart/mmx-pwa/standalone/apps/web/public/sw.js"
printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$SANDBOX/newart/mmx-pwa/standalone/apps/web/.next/server/app/manifest.webmanifest.body"
# Failure injection: gh serves the new artifact; systemctl fails on restart;
# curl answers health from CURRENT (so post-restart health for NEW fails
# while the restored OLD tree passes).
cat > "$BIN_DIR/gh" <<'GHEOF'
#!/usr/bin/env bash
set -euo pipefail
dest="${*: -1}"
case "$*" in
  *"mmx-pwa-"*) rm -rf "$dest"; mkdir -p "$dest"; cp -a "$FIXTURE_NEWART/mmx-pwa/." "$dest/" ;;
  *) printf 'unexpected gh call: %s\n' "$*" >&2; exit 2 ;;
esac
GHEOF
chmod +x "$BIN_DIR/gh"
export FIXTURE_NEWART="$SANDBOX/newart"
cat > "$BIN_DIR/systemctl" <<'SHEOF'
#!/usr/bin/env bash
if [ "${FAIL_RESTART:-0}" = 1 ]; then echo "injected restart failure" >&2; exit 1; fi
exit 0
SHEOF
chmod +x "$BIN_DIR/systemctl"
cat > "$BIN_DIR/curl" <<'CUEOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
case "$url" in
  *"/manifest.webmanifest") printf '{"id":"/?mmx-install","name":"MMX"}\n' ;;
  *"/sw.js")
    if [ "${FAIL_SW:-0}" = 1 ]; then printf 'const VERSION = "dev";\n'; else cat "$LANE_ROOT/mmx-pwa/current/apps/web/public/sw.js"; fi ;;
  *) printf 'unexpected curl url: %s\n' "$url" >&2; exit 2 ;;
esac
CUEOF
chmod +x "$BIN_DIR/curl"
export RUN_ID=123 AUTODEPLOY_REPO=x/y
fail() { echo "FAILED: $1" >&2; return 1; }
# Case A: health for the NEW tree fails -> full restore of symlink, marker, env.
export FAIL_SW=1
if converge_mmx_pwa "$NEW_SHA" "$RUN_ID"; then echo "converge must fail when new-tree health fails" >&2; exit 1; fi
test "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA"
test "$(cat "$LANE_ROOT/mmx-pwa/previous-release")" = "PRIOR_MARKER_CONTENT"
test "$(cat "$LANE_ROOT/mmx-pwa/env")" = "PRIOR_ENV_CONTENT"
# The failed NEW release dir exists (installed) but is NOT current.
test -d "$LANE_ROOT/mmx-pwa/releases/$NEW_SHA"
unset FAIL_SW
# Case B: restart itself fails -> same full restore.
export FAIL_RESTART=1
if converge_mmx_pwa "$NEW_SHA" "$RUN_ID"; then echo "converge must fail when restart fails" >&2; exit 1; fi
test "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$LANE_ROOT/mmx-pwa/releases/$OLD_SHA"
test "$(cat "$LANE_ROOT/mmx-pwa/previous-release")" = "PRIOR_MARKER_CONTENT"
test "$(cat "$LANE_ROOT/mmx-pwa/env")" = "PRIOR_ENV_CONTENT"
unset FAIL_RESTART
printf 'autodeploy mmx-pwa tests passed\n'
