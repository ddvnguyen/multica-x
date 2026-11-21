#!/usr/bin/env bash
# scripts/lane-z/autodeploy-mmx-switch.test.sh — M-482 regression test.
#
# The switched lane serves multica_mmx @ 55434 (via $LANE_ROOT/data/mmx-switch.env,
# sourced by deploy/dsh-z/deploy.sh). autodeploy.sh must use the same
# environment, and the migrate-target guard must fail when the two disagree.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX="$REPO_ROOT/.autodeploy-mmx-switch-test.$$"
trap 'rm -rf "$SANDBOX"' EXIT
mkdir -p "$SANDBOX"

log() { printf '%s\n' "$*"; }
export LANE_ROOT="$SANDBOX/lane"
mkdir -p "$LANE_ROOT/data" "$LANE_ROOT/multica-be"
# shellcheck source=./lane-lib.sh
. "$REPO_ROOT/scripts/lane-z/lane-lib.sh"

pass=0
fail=0
check() { # $1 = description; rest = command
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL: %s\n' "$desc"
  fi
}
check_fails() { # $1 = description; rest = command (must exit non-zero)
  local desc="$1"; shift
  if "$@" >/dev/null 2>&1; then
    fail=$((fail + 1))
    printf 'FAIL (expected non-zero): %s\n' "$desc"
  else
    pass=$((pass + 1))
  fi
}

# 1. autodeploy.sh sources the MMX switch override (same as deploy.sh).
grep -q 'mmx-switch\.env' "$REPO_ROOT/scripts/lane-z/autodeploy.sh" \
  || { printf 'FAIL: autodeploy.sh does not source mmx-switch.env\n'; fail=$((fail + 1)); }
pass=$((pass + 1))
grep -q 'mmx-switch\.env' "$REPO_ROOT/deploy/dsh-z/deploy.sh" \
  || { printf 'FAIL: deploy.sh reference missing\n'; fail=$((fail + 1)); }
pass=$((pass + 1))

# 2. Endpoint parsing ignores credentials and query strings.
test "$(db_url_endpoint 'postgres://multica:PASSWORD@127.0.0.1:55434/multica_mmx?sslmode=disable')" = "127.0.0.1:55434/multica_mmx" \
  || { printf 'FAIL: db_url_endpoint with placeholder password\n'; fail=$((fail + 1)); }
pass=$((pass + 1))
test "$(db_url_endpoint 'postgres://multica:secret@127.0.0.1:5432/multica_z?sslmode=disable')" = "127.0.0.1:5432/multica_z" \
  || { printf 'FAIL: db_url_endpoint with real password\n'; fail=$((fail + 1)); }
pass=$((pass + 1))

# 3. Guard passes when the migrate target is the serving DB (switched lane).
printf 'DATABASE_URL=postgres://multica:secret@127.0.0.1:55434/multica_mmx?sslmode=disable\n' > "$LANE_ROOT/multica-be/env"
check "guard passes on matching multica_mmx target" \
  assert_migrate_target_matches_serving_db 'postgres://multica:PASSWORD@127.0.0.1:55434/multica_mmx?sslmode=disable'

# 4. Guard fails when autodeploy would migrate the wrong DB (M-469 rollback).
check_fails "guard fails on multica_z target while serving multica_mmx" \
  assert_migrate_target_matches_serving_db 'postgres://multica_z:PASSWORD@127.0.0.1:5432/multica_z?sslmode=disable'

# 5. Guard passes on first provision (no serving env yet).
rm -f "$LANE_ROOT/multica-be/env"
check "guard passes with no serving env" \
  assert_migrate_target_matches_serving_db 'postgres://multica_z:PASSWORD@127.0.0.1:5432/multica_z?sslmode=disable'

# 6. Override sourcing repoints the template (deploy.sh parity).
cat > "$LANE_ROOT/data/mmx-switch.env" <<'EOF'
MULTICA_Z_DB_CONTAINER=multica-pg17-mmx
MULTICA_Z_DB_HOST=127.0.0.1
MULTICA_Z_DB_PORT=55434
MULTICA_Z_DB_NAME=multica_mmx
MULTICA_Z_DB_ROLE=multica
MULTICA_Z_DSN_TEMPLATE=postgres://multica:PASSWORD@127.0.0.1:55434/multica_mmx?sslmode=disable
EOF
# shellcheck source=/dev/null
. "$LANE_ROOT/data/mmx-switch.env"
test "$MULTICA_Z_DB_NAME" = "multica_mmx" && test "$MULTICA_Z_DB_PORT" = "55434" \
  || { printf 'FAIL: override did not repoint DB vars\n'; fail=$((fail + 1)); }
pass=$((pass + 1))
test "$(db_url_endpoint "$MULTICA_Z_DSN_TEMPLATE")" = "127.0.0.1:55434/multica_mmx" \
  || { printf 'FAIL: override template endpoint\n'; fail=$((fail + 1)); }
pass=$((pass + 1))
# With the serving env back on multica_mmx, the overridden template passes.
printf 'DATABASE_URL=postgres://multica:secret@127.0.0.1:55434/multica_mmx?sslmode=disable\n' > "$LANE_ROOT/multica-be/env"
check "guard passes with override template against serving multica_mmx" \
  assert_migrate_target_matches_serving_db "$MULTICA_Z_DSN_TEMPLATE"

if [ "$fail" -ne 0 ]; then
  printf 'autodeploy mmx-switch tests FAILED (%d passed, %d failed)\n' "$pass" "$fail"
  exit 1
fi
printf 'autodeploy mmx-switch tests passed (%d checks)\n' "$pass"
