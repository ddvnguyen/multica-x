#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$REPO_ROOT/.autodeploy-safety-test.$$"
trap 'rm -rf "$SANDBOX"' EXIT
mkdir -p "$SANDBOX/bin" "$SANDBOX/mock-bin"
cp "$REPO_ROOT/scripts/lane-z/autodeploy.sh" "$REPO_ROOT/scripts/lane-z/lane-lib.sh" "$SANDBOX/bin/"

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
DSH_Z_WEB_UNIT=dsh-z.service
MULTICA_BE_Z_PORT=3180
MULTICA_Z_DB_NAME=multica_z
MULTICA_Z_DB_CONTAINER=multica-z-db
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
  mkdir -p "$TEST_LANE_ROOT/multica-be/releases/current" "$TEST_LANE_ROOT/data" "$TEST_LANE_ROOT/backups"
  printf '{"sha":"%s","version":"test"}\n' "$CURRENT_SHA" > "$TEST_LANE_ROOT/multica-be/releases/current/release.json"
  ln -s "$TEST_LANE_ROOT/multica-be/releases/current" "$TEST_LANE_ROOT/multica-be/current"
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
