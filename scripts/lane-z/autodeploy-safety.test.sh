#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$REPO_ROOT/.autodeploy-safety-test.$$"
trap 'rm -rf "$SANDBOX"' EXIT
mkdir -p "$SANDBOX/bin" "$SANDBOX/mock-bin"
cp "$REPO_ROOT/scripts/lane-z/autodeploy.sh" "$REPO_ROOT/scripts/lane-z/rollback.sh" "$REPO_ROOT/scripts/lane-z/lane-lib.sh" "$SANDBOX/bin/"
# Operators run rollback.sh directly; without a bash shebang it would run
# under the caller's shell (M-451 Architect gate).
test "$(head -1 "$REPO_ROOT/scripts/lane-z/rollback.sh")" = '#!/usr/bin/env bash'
cat > "$SANDBOX/bin/dsh-z.env" <<'EOF'
LANE_ROOT="$TEST_LANE_ROOT"
AUTODEPLOY_LOG_DIR="$TEST_LANE_ROOT/logs"
AUTODEPLOY_BACKUP_DIR="$TEST_LANE_ROOT/backups"
AUTODEPLOY_REPORT_DIR="$TEST_LANE_ROOT/reports"
AUTODEPLOY_WEBHOOK_FILE="$TEST_LANE_ROOT/webhook"
AUTODEPLOY_REPO=ddvnguyen/multica-x
AUTODEPLOY_BRANCH=multica-moct
AUTODEPLOY_WORKFLOW=CI
AUTODEPLOY_MIN_FREE_GB=0
AUTODEPLOY_KEEP_RELEASES=5
AUTODEPLOY_BACKUP_KEEP_COUNT=10
MULTICA_BE_Z_UNIT=multica-be-z.service
MMX_PWA_Z_PORT=3190
MMX_PWA_Z_UNIT=mmx-pwa-z.service
DSH_Z_WEB_UNIT=dsh-z.service
DSH_Z_PLUGIN_BUNDLE="$TEST_LANE_ROOT/bundles/plugin"
MULTICA_BE_Z_PORT=3180
MULTICA_Z_DB_NAME=multica_z
MULTICA_Z_DB_CONTAINER=multica-z-db
MULTICA_Z_DB_HOST=127.0.0.1
MULTICA_Z_DB_PORT=5432
MULTICA_Z_DSN_TEMPLATE=postgres://multica_z:PASSWORD@127.0.0.1:5432/multica_z?sslmode=disable
EOF

cat > "$SANDBOX/mock-bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$1 $2" in
  "run list")
    printf '[{"headSha":"%s","databaseId":123,"createdAt":"2026-11-15T00:00:00Z","updatedAt":"2026-11-15T00:01:00Z","url":"https://example.invalid/run/123"}]\n' "$TEST_TARGET_SHA"
    ;;
  "api "*)
    case "${2:-}" in
      repos/*/compare/*)
        [ "${TEST_COMPARE_ERROR:-0}" = 0 ] || exit 1
        printf '{"status":"%s"}\n' "$TEST_COMPARE_STATUS"
        ;;
      repos/*/actions/runs/*/artifacts) printf '{"artifacts":[]}\n' ;;
      repos/*/commits/*/pulls) printf '[]\n' ;;
      *) printf '{}\n' ;;
    esac
    ;;
  "run download")
    [ "${TEST_FAIL_DOWNLOAD:-0}" = 0 ] || exit 1
    # Materialize a fake tree per artifact name so verify-artifact and the
    # staged installs exercise real files: backend gets bin/server +
    # migrations + release.json, PWA gets standalone + brand + stamp.
    dest="${*: -1}"
    name=""
    for arg in "$@"; do
      case "$arg" in -n) want_name=1;; *) [ "${want_name:-0}" = 1 ] && { name="$arg"; want_name=0; };; esac
    done
    sha="${name##*-}"
    case "$name" in
      multica-be-*)
        mkdir -p "$dest/bin" "$dest/migrations"
        printf '#!/bin/sh\nexit 0\n' > "$dest/bin/server"; chmod +x "$dest/bin/server"
        printf '#!/bin/sh\nexit 0\n' > "$dest/bin/migrate"; chmod +x "$dest/bin/migrate"
        # Lane CLI: the deployed daemon reports running this artifact's SHA,
        # idle — so the daemon-refresh gate passes with no restart and no poll.
        printf '#!/usr/bin/env bash\nprintf \x27{"status":"running","cli_commit":"%s","active_task_count":0}\x27\n' "$sha" > "$dest/bin/multica"
        chmod +x "$dest/bin/multica"
        printf '{"sha":"%s","version":"test","builtAt":"test"}\n' "$sha" > "$dest/release.json"
        ;;
      mmx-pwa-*)
        # Mirror the real CI multi-path layout: apps/web/.next/standalone,
        # not a flat standalone/ dir — the resolver must find it.
        mkdir -p "$dest/apps/web/.next/standalone/apps/web/public" "$dest/apps/web/.next/standalone/apps/web/.next/server/app"
        printf 'server\n' > "$dest/apps/web/.next/standalone/apps/web/server.js"
        printf 'const VERSION = "%s";\n' "$sha" > "$dest/apps/web/.next/standalone/apps/web/public/sw.js"
        printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$dest/apps/web/.next/standalone/apps/web/.next/server/app/manifest.webmanifest.body"
        printf '{"sha":"%s","version":"test"}\n' "$sha" > "$dest/release.json"
        ;;
    esac
    ;;
  "run view") printf '{"headSha":"%s","url":"https://example.invalid/run/123","createdAt":"2026-11-15T00:00:00Z","updatedAt":"2026-11-15T00:01:00Z","conclusion":"success"}\n' "$TEST_TARGET_SHA" ;;
  *) printf 'unexpected gh call: %s\n' "$*" >&2; exit 2 ;;
esac
EOF
chmod +x "$SANDBOX/mock-bin/gh"

CURRENT_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
TARGET_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
export PATH="$SANDBOX/mock-bin:$PATH" TEST_TARGET_SHA="$TARGET_SHA"
new_lane() {
  TEST_LANE_ROOT="$SANDBOX/lane-$1"
  export TEST_LANE_ROOT
  mkdir -p "$TEST_LANE_ROOT/multica-be/releases/current" "$TEST_LANE_ROOT/mmx-pwa/releases/current/apps/web/public" "$TEST_LANE_ROOT/mmx-pwa/releases/current/apps/web/.next/server/app" "$TEST_LANE_ROOT/data" "$TEST_LANE_ROOT/backups"
  printf '{"sha":"%s","version":"test"}\n' "$CURRENT_SHA" > "$TEST_LANE_ROOT/multica-be/releases/current/release.json"
  ln -s "$TEST_LANE_ROOT/multica-be/releases/current" "$TEST_LANE_ROOT/multica-be/current"
  printf 'server\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/current/apps/web/server.js"
  printf 'const VERSION = "%s";\n' "$CURRENT_SHA" > "$TEST_LANE_ROOT/mmx-pwa/releases/current/apps/web/public/sw.js"
  printf '{"id":"/?mmx-install","name":"MMX"}\n' > "$TEST_LANE_ROOT/mmx-pwa/releases/current/apps/web/.next/server/app/manifest.webmanifest.body"
  ln -s "$TEST_LANE_ROOT/mmx-pwa/releases/current" "$TEST_LANE_ROOT/mmx-pwa/current"
}

# A stale green run exits successfully before artifacts, reports, or backup work.
new_lane older
export TEST_COMPARE_STATUS=behind TEST_COMPARE_ERROR=0 TEST_FAIL_DOWNLOAD=0
"$SANDBOX/bin/autodeploy.sh" --dry-run > "$SANDBOX/older.log" 2>&1
grep -q 'target is behind running release; skipping' "$SANDBOX/older.log"
! grep -q 'dry-run: artifacts in run' "$SANDBOX/older.log"
test ! -e "$TEST_LANE_ROOT/data/autodeploy-comment.md"
test -z "$(find "$TEST_LANE_ROOT/backups" -type f -print -quit)"
test -z "$(find "$TEST_LANE_ROOT/reports" -type f -print -quit 2>/dev/null || true)"

# A compare API outage is also a clean skip and never marks the target failed.
new_lane compare-error
export TEST_COMPARE_ERROR=1
"$SANDBOX/bin/autodeploy.sh" --dry-run > "$SANDBOX/compare-error.log" 2>&1
grep -q 'could not compare target .*; skipping' "$SANDBOX/compare-error.log"
test ! -e "$TEST_LANE_ROOT/data/autodeploy.failed"
export TEST_COMPARE_ERROR=0

# A descendant target reaches dry-run planning and still touches no live service.
new_lane ahead
export TEST_COMPARE_STATUS=ahead
"$SANDBOX/bin/autodeploy.sh" --dry-run > "$SANDBOX/ahead.log" 2>&1
grep -q 'dry-run: newest green run' "$SANDBOX/ahead.log"
grep -q 'dry-run: would download' "$SANDBOX/ahead.log"
test ! -e "$TEST_LANE_ROOT/data/autodeploy-comment.md"
test -z "$(find "$TEST_LANE_ROOT/backups" -type f -print -quit)"

# The existing failed-SHA guard runs before compare or artifact lookup.
new_lane marked
printf '%s\n' "$TARGET_SHA" > "$TEST_LANE_ROOT/data/autodeploy.failed"
"$SANDBOX/bin/autodeploy.sh" --dry-run > "$SANDBOX/marked.log" 2>&1
grep -q 'already failed on this host; not retrying' "$SANDBOX/marked.log"
! grep -q 'dry-run: artifacts in run' "$SANDBOX/marked.log"

# An actual deployment-path failure under mocks records the SHA centrally.
new_lane failed
export TEST_COMPARE_STATUS=ahead TEST_FAIL_DOWNLOAD=1
set +e
"$SANDBOX/bin/autodeploy.sh" > "$SANDBOX/failed.log" 2>&1
rc=$?
set -e
test "$rc" -ne 0
test "$(cat "$TEST_LANE_ROOT/data/autodeploy.failed")" = "$TARGET_SHA"
# Full deploy path under mocks: staged installs, migration (mocked), switch,
# health (mocked), report. Only podman (DB), systemctl (units) and curl (HTTP)
# are mocked — jq, df, gh and the rest are real so date math, release.json
# reads and sha comparisons exercise the production code paths.
new_lane full
export TEST_COMPARE_STATUS=ahead TEST_FAIL_DOWNLOAD=0
mkdir -p "$SANDBOX/mock-full"
cat > "$SANDBOX/mock-full/podman" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *"pg_dump"*) printf 'fake-dump\n' ;;
  *"select version from schema_migrations"*) printf '001\n' ;;
  *) exit 0 ;;
esac
MOCKEOF
# systemctl: restarts succeed; `unit_release` (`systemctl --user cat`) reports
# the ExecStart of whatever mmx-pwa/current or multica-be/current resolves
# to, like a unit file following the symlink would.
cat > "$SANDBOX/mock-full/systemctl" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *"show --property=MainPID"*) printf '1\n' ;;
  *"cat multica-be-z.service"*) printf 'ExecStart=%s/bin/server\n' "$TEST_LANE_ROOT/multica-be/current" ;;
  # A PWA restart stamps the mock's dead window (M-459); the curl mock below
  # refuses connections until it closes, as a restarted Next server does.
  *"restart mmx-pwa-z.service"*) [ -z "${PWA_RESTART_STAMP:-}" ] || date +%s > "$PWA_RESTART_STAMP"; exit 0 ;;
  *) exit 0 ;;
esac
MOCKEOF
# curl: every health endpoint answers from the CURRENT lane state, like the
# real lane would — backend healthz ok, manifests from disk. An unknown route
# is a 404 (curl -f's non-zero exit): the lane serves no /moct/status, so a
# gate that probes one must fail here instead of passing on a mock (M-458).
cat > "$SANDBOX/mock-full/curl" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
if [ -n "${PWA_RESTART_STAMP:-}" ] && [ -f "$PWA_RESTART_STAMP" ] \
  && [ "$(( $(date +%s) - $(cat "$PWA_RESTART_STAMP") ))" -lt "${PWA_DOWN_SECONDS:-0}" ]; then
  printf 'curl: (7) Failed to connect to 127.0.0.1 port 3190\n' >&2
  exit 7
fi
case "$url" in
  *"/multica/healthz"*) printf '{"status":"ok"}\n' ;;
  *"/multica/health"*) printf '{"commit":"%s","status":"ok"}\n' "$(jq -r '.sha' "$TEST_LANE_ROOT/multica-be/current/release.json")" ;;
  *"/healthz"*) printf '{"status":"ok"}\n' ;;
  *"/manifest.webmanifest"*) cat "$TEST_LANE_ROOT/mmx-pwa/current/apps/web/../manifest.json" 2>/dev/null || printf '{"id":"/?mmx-install","name":"MMX"}\n' ;;
  *"/sw.js"*) cat "$TEST_LANE_ROOT/mmx-pwa/current/apps/web/public/sw.js" 2>/dev/null ;;
  */) printf 'ok' ;;
  *) printf 'curl: (22) 404 %s\n' "$url" >&2; exit 22 ;;
esac
MOCKEOF
chmod +x "$SANDBOX/mock-full/podman" "$SANDBOX/mock-full/systemctl" "$SANDBOX/mock-full/curl"
export PATH="$SANDBOX/mock-full:$SANDBOX/mock-bin:$PATH"
export LANE_RUNTIME_ENV="$TEST_LANE_ROOT/runtime.env"
export MULTICA_Z_DEV_CODE=123456 MULTICA_Z_DB_PASSWORD=test MULTICA_Z_PROFILE=z
export LANE_HOME="$TEST_LANE_ROOT/home"
export LANE_ROOT="$TEST_LANE_ROOT" MULTICA_Z_DB_CONTAINER=x MULTICA_Z_DSN_TEMPLATE='postgres://u:PASSWORD@h/db'
export MULTICA_Z_DB_NAME=multica_z AUTODEPLOY_BIN_DIR="$SANDBOX/bin"
export HOME="$SANDBOX/home-full"; mkdir -p "$HOME/.config/systemd/user"
# The artifact's lane CLI (see the mock above) reports the deployed SHA once
# the new release is live, so the daemon-refresh gate passes with no restart.
"$SANDBOX/bin/autodeploy.sh" > "$SANDBOX/full.log" 2>&1
grep -q 'mmx-pwa artifact verified' "$SANDBOX/full.log"
grep -q 'backend release staged' "$SANDBOX/full.log"
grep -q 'mmx-pwa release staged' "$SANDBOX/full.log"
grep -q 'mmx-pwa z -> releases/' "$SANDBOX/full.log"
# Both live trees flipped to the target SHA; markers name the previous release.
test "$(readlink -f "$TEST_LANE_ROOT/multica-be/current")" = "$TEST_LANE_ROOT/multica-be/releases/$TARGET_SHA"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA"
test "$(cat "$TEST_LANE_ROOT/multica-be/previous-release")" = "$TEST_LANE_ROOT/multica-be/releases/current"
test "$(cat "$TEST_LANE_ROOT/mmx-pwa/previous-release")" = "$TEST_LANE_ROOT/mmx-pwa/releases/current"
# Env follows the target SHA; the staged temp dirs are gone.
grep -qx "NEXT_PUBLIC_APP_VERSION=$TARGET_SHA" "$TEST_LANE_ROOT/mmx-pwa/env"
test -z "$(find "$TEST_LANE_ROOT/multica-be/releases" "$TEST_LANE_ROOT/mmx-pwa/releases" -maxdepth 1 -name '*.tmp.*' -print -quit)"
grep -q 'mmx-pwa:' "$TEST_LANE_ROOT/reports/latest.md"
grep -q "lane z is on \`${TARGET_SHA:0:9}\`" "$TEST_LANE_ROOT/reports/latest.md"
# The evidence must not claim a /moct/status that was never probed.
! grep -q 'moct/status' "$TEST_LANE_ROOT/reports/latest.md"

# M-459: the same switch against a PWA that refuses connections for a moment
# after its restart. The post-switch gate must wait it out; the single-shot
# gate this replaces failed the live tick for ab4f636b at exactly this point.
new_lane pwa-race
export PWA_RESTART_STAMP="$TEST_LANE_ROOT/pwa-restarted.at" PWA_DOWN_SECONDS=1
start="$(date +%s)"
"$SANDBOX/bin/autodeploy.sh" > "$SANDBOX/pwa-race.log" 2>&1
grep -q 'step: switch-mmx-pwa' "$SANDBOX/pwa-race.log"
grep -q 'mmx-pwa health ok: brand MMX' "$SANDBOX/pwa-race.log"
test "$(readlink -f "$TEST_LANE_ROOT/mmx-pwa/current")" = "$TEST_LANE_ROOT/mmx-pwa/releases/$TARGET_SHA"
test "$(( $(date +%s) - start ))" -lt 120
! grep -q 'manifest did not answer' "$SANDBOX/pwa-race.log"
unset PWA_RESTART_STAMP PWA_DOWN_SECONDS
export PATH="$SANDBOX/mock-bin:$PATH"

# M-458 regression: the post-switch gate passes with the lane web answering
# only /healthz, /multica/healthz and /multica/health. /moct/status is not a
# server route (the real one is authenticated under /multica/api/...), so it
# 404s; a reintroduced probe must fail here rather than on a healthy lane.
new_lane health-gate
mkdir -p "$SANDBOX/mock-gate"
cat > "$SANDBOX/mock-gate/curl" <<'MOCKEOF'
#!/usr/bin/env bash
set -euo pipefail
url="${*: -1}"
printf '%s\n' "$url" >> "$TEST_LANE_ROOT/curl.log"
case "$url" in
  *"/healthz") printf '{"status":"ok"}\n' ;;
  *"/multica/healthz") printf '{"status":"ok"}\n' ;;
  *"/multica/health") printf '{"commit":"%s"}\n' "$(jq -r '.sha' "$TEST_LANE_ROOT/multica-be/current/release.json")" ;;
  *) printf 'curl: (22) 404 %s\n' "$url" >&2; exit 22 ;;
esac
MOCKEOF
chmod +x "$SANDBOX/mock-gate/curl"
export PATH="$SANDBOX/mock-gate:$SANDBOX/mock-bin:$PATH"
export MULTICA_BE_Z_PORT=3180 DRILL_FAIL_HEALTH=0
log() { printf '%s\n' "$*"; }
source "$SANDBOX/bin/lane-lib.sh"
sed -n '/^assert_health() {/,/^}/p' "$SANDBOX/bin/autodeploy.sh" > "$SANDBOX/assert-health-function.sh"
source "$SANDBOX/assert-health-function.sh"
assert_health "$CURRENT_SHA"
test "$LANE_COMMIT" = "$CURRENT_SHA"
test -s "$TEST_LANE_ROOT/curl.log"
! grep -q 'moct/status' "$TEST_LANE_ROOT/curl.log"
export PATH="$SANDBOX/mock-bin:$PATH"

# Exercise the real pruning function with synthetic timestamps. The old dump
# for the running release survives alongside the two newest other dumps.
new_lane prune
export AUTODEPLOY_BACKUP_DIR="$TEST_LANE_ROOT/backups" AUTODEPLOY_BACKUP_KEEP_COUNT=2 DRY_RUN=0
log() { printf '%s\n' "$*"; }
source "$SANDBOX/bin/lane-lib.sh"
release_sha() { jq -r '.sha // "none"' "$TEST_LANE_ROOT/multica-be/current/release.json"; }
sed -n '/^prune_backups() {/,/^}/p' "$SANDBOX/bin/autodeploy.sh" > "$SANDBOX/prune-function.sh"
source "$SANDBOX/prune-function.sh"
backup() {
  local sha="$1" stamp="$2" file
  file="$TEST_LANE_ROOT/backups/multica_z-pre-${sha}-${stamp}.dump"
  : > "$file"
  : > "$TEST_LANE_ROOT/backups/multica_z-pre-${sha}-migrations-${stamp}.txt"
  touch -t "$3" "$file"
}
backup "${CURRENT_SHA:0:9}" 20261110T000000Z 202611100000
backup ccccccccc 20261111T000000Z 202611110000
backup ddddddddd 20261112T000000Z 202611120000
backup eeeeeeeee 20261113T000000Z 202611130000
prune_backups > "$SANDBOX/prune.log"
test -f "$TEST_LANE_ROOT/backups/multica_z-pre-${CURRENT_SHA:0:9}-20261110T000000Z.dump"
test -f "$TEST_LANE_ROOT/backups/multica_z-pre-ddddddddd-20261112T000000Z.dump"
test -f "$TEST_LANE_ROOT/backups/multica_z-pre-eeeeeeeee-20261113T000000Z.dump"
test ! -e "$TEST_LANE_ROOT/backups/multica_z-pre-ccccccccc-20261111T000000Z.dump"
test ! -e "$TEST_LANE_ROOT/backups/multica_z-pre-ccccccccc-migrations-20261111T000000Z.txt"

printf 'autodeploy safety tests passed\n'
