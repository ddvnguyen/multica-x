#!/usr/bin/env bash
# Tests for deploy/dsh-z/switch-to-mmx.sh. Everything runs in a sandbox under
# the repo dir; podman and systemctl are stubbed. Never touches the lane.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$REPO_ROOT/deploy/dsh-z/switch-to-mmx.sh"
SANDBOX="$(mktemp -d "$REPO_ROOT/.switch-test.XXXXXX")"
trap 'rm -rf "$SANDBOX"' EXIT

STUB="$SANDBOX/bin"
mkdir -p "$STUB"

# podman stub: inspect ok, psql quiescence 0; pg_dump fails when told to.
cat > "$STUB/podman" <<'EOF'
#!/usr/bin/env bash
if [ "${MMX_TEST_DUMP_FAIL:-0}" = "1" ] && [[ "$*" == *pg_dump* ]]; then
  echo "stub: pg_dump failure" >&2
  exit 1
fi
case "$*" in
  *inspect*) printf '{"State":{"Status":"running"}}\n' ;;
  *) printf '0\n' ;;
esac
EOF

# systemctl stub: record every call.
cat > "$STUB/systemctl" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MMX_TEST_SYSTEMCTL_LOG"
EOF

# curl stub: health check always succeeds.
cat > "$STUB/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

chmod +x "$STUB/podman" "$STUB/systemctl" "$STUB/curl"
export PATH="$STUB:$PATH"

export LANE_ROOT="$SANDBOX/lane"
export LANE_RUNTIME_ENV="$SANDBOX/lane-runtime.env"
export MMX_LANE_ROOT="$LANE_ROOT"
export MMX_LANE_RUNTIME_ENV="$LANE_RUNTIME_ENV"
export MMX_TEST_SYSTEMCTL_LOG="$SANDBOX/systemctl.log"
mkdir -p "$LANE_ROOT/multica-be" "$LANE_ROOT/data"
printf 'PORT=4390\nJWT_SECRET=lane-jwt-secret\nDATABASE_URL=postgres://multica_z:old-secret@127.0.0.1:5432/multica_z?sslmode=disable\nLOCAL_UPLOAD_DIR=/mnt/workspace/clouds/dsh-z/uploads-old\n' > "$LANE_ROOT/multica-be/env"
printf 'MULTICA_Z_DB_PASSWORD=old-secret\n' > "$LANE_RUNTIME_ENV"
export MULTICA_Z_DB_CONTAINER=infra-postgres
export MULTICA_Z_DB_NAME=multica_z
export MULTICA_MMX_PG17_CONTAINER=multica-pg17-mmx
export MULTICA_MMX_PG17_PORT=55434
export MULTICA_MMX_PG17_PW_FILE="$SANDBOX/pw"
printf 'secret' > "$SANDBOX/pw"
export MULTICA_MMX_PG17_UPLOADS="$SANDBOX/uploads"
export MULTICA_MMX_SWITCH_BACKUP_ROOT="$SANDBOX/backups"

OVERRIDE="$LANE_ROOT/data/mmx-switch.env"
fail() { echo "FAIL: $*"; exit 1; }

# --- 1: switch dry-run previews and changes nothing
out="$(bash "$SCRIPT" --dry-run)"
grep -q "dry-run complete" <<<"$out" || fail "switch dry-run preview missing: $out"
[ ! -e "$OVERRIDE" ] || fail "switch dry-run wrote the override file"

# --- 2: --rollback <dir> --dry-run exits 0 without changes
BDIR="$SANDBOX/backups/rb"
mkdir -p "$BDIR"
cp "$LANE_ROOT/multica-be/env" "$BDIR/multica-be-env"
cp "$LANE_RUNTIME_ENV" "$BDIR/lane-runtime-env"
printf 'dump' > "$BDIR/multica_z.dump"
( cd "$BDIR" && sha256sum multica-be-env lane-runtime-env multica_z.dump > sha256sums.txt )
printf 'MULTICA_Z_DB_PASSWORD=secret\n' > "$OVERRIDE"
before_be="$(cat "$LANE_ROOT/multica-be/env")"
before_rt="$(cat "$LANE_RUNTIME_ENV")"
for order in --rollback-first --dry-run-first; do
  if [ "$order" = "--rollback-first" ]; then
    out="$(bash "$SCRIPT" --rollback "$BDIR" --dry-run)"
  else
    out="$(bash "$SCRIPT" --dry-run --rollback "$BDIR")"
  fi
  grep -q "rollback dry-run complete" <<<"$out" || fail "rollback dry-run ($order) preview missing: $out"
  grep -q 'MULTICA_Z_DB_PASSWORD=secret' "$OVERRIDE" || fail "rollback dry-run ($order) removed the override file"
  [ "$(cat "$LANE_ROOT/multica-be/env")" = "$before_be" ] || fail "rollback dry-run ($order) modified the lane env"
  [ "$(cat "$LANE_RUNTIME_ENV")" = "$before_rt" ] || fail "rollback dry-run ($order) modified the runtime env"
  [ ! -s "$MMX_TEST_SYSTEMCTL_LOG" ] || fail "rollback dry-run ($order) restarted a unit"
done

# --- 3: --rollback without an argument fails
if bash "$SCRIPT" --rollback >/dev/null 2>&1; then
  fail "--rollback without a directory argument must fail"
fi

# --- 4: rollback restores the env files, removes the override, restarts the unit
printf 'CHANGED=1\n' > "$LANE_ROOT/multica-be/env"
printf 'MULTICA_Z_DB_PASSWORD=new-secret\n' > "$LANE_RUNTIME_ENV"
: > "$MMX_TEST_SYSTEMCTL_LOG"
out="$(bash "$SCRIPT" --rollback "$BDIR")"
grep -q "rollback complete" <<<"$out" || fail "rollback run failed: $out"
grep -q 'LOCAL_UPLOAD_DIR=/mnt/workspace/clouds/dsh-z/uploads-old' "$LANE_ROOT/multica-be/env" \
  || fail "rollback did not restore the lane env"
grep -q 'MULTICA_Z_DB_PASSWORD=old-secret' "$LANE_RUNTIME_ENV" \
  || fail "rollback did not restore the runtime env"
[ ! -e "$OVERRIDE" ] || fail "rollback did not remove the override file"
grep -q "restart ${MULTICA_BE_Z_UNIT:-multica-be-z.service}" "$MMX_TEST_SYSTEMCTL_LOG" \
  || fail "rollback did not restart the backend unit: $(cat "$MMX_TEST_SYSTEMCTL_LOG")"

# --- 5: a failing multica_z dump aborts the switch with no override written
export MMX_TEST_DUMP_FAIL=1
if bash "$SCRIPT" >/dev/null 2>&1; then
  fail "switch must abort when the multica_z dump fails"
fi
[ ! -e "$OVERRIDE" ] || fail "override written despite dump failure"
grep -q 'MULTICA_Z_DB_PASSWORD=old-secret' "$LANE_RUNTIME_ENV" \
  || fail "runtime env modified despite dump failure"
unset MMX_TEST_DUMP_FAIL

# --- 6: a successful switch rewrites the lane env and never echoes the password
: > "$MMX_TEST_SYSTEMCTL_LOG"
out="$(bash "$SCRIPT")"
grep -q "switch complete" <<<"$out" || fail "switch run failed: $out"
if grep -q "secret" <<<"$out"; then fail "switch output echoed the password"; fi
grep -q "DATABASE_URL=postgres://multica:secret@127.0.0.1:55434/multica_mmx?sslmode=disable" "$LANE_ROOT/multica-be/env" \
  || fail "lane env DATABASE_URL not rewritten: $(grep '^DATABASE_URL=' "$LANE_ROOT/multica-be/env")"
grep -q "LOCAL_UPLOAD_DIR=$SANDBOX/uploads" "$LANE_ROOT/multica-be/env" \
  || fail "lane env LOCAL_UPLOAD_DIR not rewritten"
grep -q "JWT_SECRET=lane-jwt-secret" "$LANE_ROOT/multica-be/env" \
  || fail "lane JWT_SECRET not preserved"
grep -q "PORT=4390" "$LANE_ROOT/multica-be/env" || fail "PORT not preserved"
grep -q "MULTICA_Z_DB_PASSWORD=secret" "$LANE_RUNTIME_ENV" \
  || fail "runtime env password not updated"
if [ ! -s "$OVERRIDE" ]; then fail "override file missing"; fi
[ "$(stat -c %a "$OVERRIDE")" = "600" ] || fail "override not mode 600"
[ "$(stat -c %a "$LANE_ROOT/multica-be/env")" = "600" ] || fail "lane env not mode 600"
[ "$(stat -c %a "$LANE_RUNTIME_ENV")" = "600" ] || fail "runtime env not mode 600"
grep -q "restart multica-be-z.service" "$MMX_TEST_SYSTEMCTL_LOG" \
  || fail "switch did not restart the backend unit"

echo "PASS"
