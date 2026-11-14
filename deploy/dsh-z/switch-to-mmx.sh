#!/usr/bin/env bash
# deploy/dsh-z/switch-to-mmx.sh — pilot switch of lane z to the migrated
# multica_mmx (PG17) copy. dsh-z only. Never touches main or the daemon.
#
# Usage:
#   switch-to-mmx.sh [--dry-run]              perform (or preview) the switch
#   switch-to-mmx.sh --rollback <backup-dir> [--dry-run]
#                                             restore lane z from a switch backup
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=dsh-z.env
source "$SCRIPT_DIR/dsh-z.env"

# Sandbox hooks for switch-to-mmx.test.sh: dsh-z.env is authoritative on the
# lane, but a test may relocate the lane root and runtime env.
LANE_ROOT="${MMX_LANE_ROOT:-$LANE_ROOT}"
LANE_RUNTIME_ENV="${MMX_LANE_RUNTIME_ENV:-$LANE_RUNTIME_ENV}"

PG17_CONTAINER="${MULTICA_MMX_PG17_CONTAINER:-multica-pg17-mmx}"
PG17_HOST="127.0.0.1"
PG17_PORT="${MULTICA_MMX_PG17_PORT:-55434}"
PG17_DB="${MULTICA_MMX_PG17_DB:-multica_mmx}"
PG17_ROLE="${MULTICA_MMX_PG17_ROLE:-multica}"
PG17_PW_FILE="${MULTICA_MMX_PG17_PW_FILE:-/mnt/workspace/clouds/dsh-z-pg17/pgpassword}"
PG17_UPLOADS="${MULTICA_MMX_PG17_UPLOADS:-/mnt/workspace/clouds/dsh-z-pg17/uploads}"
OVERRIDE_FILE="$LANE_ROOT/data/mmx-switch.env"
BACKUP_ROOT="${MULTICA_MMX_SWITCH_BACKUP_ROOT:-/mnt/workspace/backups/dsh-z/mmx-switch}"

log() { printf '[switch-mmx] %s\n' "$*"; }
die() { printf '[switch-mmx] ERROR: %s\n' "$*" >&2; exit 1; }

DRY_RUN=0
ROLLBACK_DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --rollback)
      [ $# -ge 2 ] || die "--rollback requires a backup directory argument"
      ROLLBACK_DIR="$2"
      shift
      ;;
    -h|--help) echo "usage: $0 [--dry-run] | [--rollback <backup-dir>] [--dry-run]"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
  shift
done

# Resolve password (never echoed)
[ -f "$PG17_PW_FILE" ] || die "PG17 password file missing: $PG17_PW_FILE"
PG17_PW="$(cat "$PG17_PW_FILE")"
export PGPASSWORD="$PG17_PW"

psql_mmx() { podman exec -e PGPASSWORD="$PG17_PW" "$PG17_CONTAINER" psql -U "$PG17_ROLE" -d "$PG17_DB" -tAc "$1"; }

# Rollback path
if [ -n "$ROLLBACK_DIR" ]; then
  [ -d "$ROLLBACK_DIR" ] || die "backup dir missing: $ROLLBACK_DIR"
  if [ "$DRY_RUN" = "1" ]; then
    log "rollback dry-run against $ROLLBACK_DIR; no changes made"
    log "dry-run: would restore $ROLLBACK_DIR/multica-be-env -> $LANE_ROOT/multica-be/env"
    log "dry-run: would restore $ROLLBACK_DIR/lane-runtime-env -> $LANE_RUNTIME_ENV"
    log "dry-run: would remove $OVERRIDE_FILE"
    log "dry-run: would restart $MULTICA_BE_Z_UNIT"
    log "rollback dry-run complete"
    exit 0
  fi
  for f in multica-be-env lane-runtime-env multica_z.dump sha256sums.txt; do
    [ -f "$ROLLBACK_DIR/$f" ] || die "backup incomplete: $ROLLBACK_DIR/$f missing"
  done
  log "rollback: verifying backup checksums in $ROLLBACK_DIR"
  ( cd "$ROLLBACK_DIR" && sha256sum -c sha256sums.txt >/dev/null ) || die "backup checksum verification failed"
  log "rollback: restoring env files from $ROLLBACK_DIR"
  cp "$ROLLBACK_DIR/multica-be-env" "$LANE_ROOT/multica-be/env"
  cp "$ROLLBACK_DIR/lane-runtime-env" "$LANE_RUNTIME_ENV"
  chmod 600 "$LANE_RUNTIME_ENV" "$LANE_ROOT/multica-be/env"
  rm -f "$OVERRIDE_FILE"
  systemctl --user restart "$MULTICA_BE_Z_UNIT"
  log "rollback complete"
  exit 0
fi

# Preflight
log "preflight: pg17 container $PG17_CONTAINER"
podman inspect "$PG17_CONTAINER" >/dev/null 2>&1 || die "pg17 container not running"

# Quiescence checks on the copy
autopilot_enabled="$(psql_mmx "SELECT count(*) FROM autopilot_trigger WHERE enabled = true;" 2>/dev/null || echo ERR)"
wakeup_active="$(psql_mmx "SELECT count(*) FROM issue_wakeup WHERE enabled = true;" 2>/dev/null || echo ERR)"
tasks_active="$(psql_mmx "SELECT count(*) FROM agent_task_queue WHERE status IN ('queued','running');" 2>/dev/null || echo ERR)"
log "quiescence: autopilot_enabled=$autopilot_enabled wakeup_active=$wakeup_active tasks_active=$tasks_active"
for v in "$autopilot_enabled" "$wakeup_active" "$tasks_active"; do
  [ "$v" = "ERR" ] && die "quiescence query failed"
  [ "$v" = "0" ] || die "quiescence gate failed: non-zero ($v)"
done

# Backup — must fully succeed (dump + verified checksums) before anything on
# the lane is touched.
TS="$(date -u +%Y%m%dT%H%M%SZ)"
BDIR="$BACKUP_ROOT/$TS"
log "backup dir: $BDIR"
if [ "$DRY_RUN" = "1" ]; then
  log "dry-run: would create $BDIR, back up env files + multica_z dump + uploads path, write override, restart $MULTICA_BE_Z_UNIT"
  log "dry-run complete"
  exit 0
fi
mkdir -p "$BDIR"
chmod 700 "$BDIR"
[ -f "$LANE_ROOT/multica-be/env" ] || die "lane env missing: $LANE_ROOT/multica-be/env"
[ -f "$LANE_RUNTIME_ENV" ] || die "lane runtime env missing: $LANE_RUNTIME_ENV"
cp "$LANE_ROOT/multica-be/env" "$BDIR/multica-be-env"
cp "$LANE_RUNTIME_ENV" "$BDIR/lane-runtime-env"
chmod 600 "$BDIR/multica-be-env" "$BDIR/lane-runtime-env"
# Record the pre-switch uploads path reference (what the lane served before).
if grep -hqE '^(LOCAL_UPLOAD_DIR|MULTICA_Z_UPLOADS_DIR)=' "$LANE_ROOT/multica-be/env" "$LANE_RUNTIME_ENV"; then
  grep -hE '^(LOCAL_UPLOAD_DIR|MULTICA_Z_UPLOADS_DIR)=' "$LANE_ROOT/multica-be/env" "$LANE_RUNTIME_ENV" > "$BDIR/uploads-path.txt"
else
  : > "$BDIR/uploads-path.txt"
fi
# multica_z dump (rollback safety net): any failure aborts before the override
# file or the lane runtime env is modified.
if ! podman exec "$MULTICA_Z_DB_CONTAINER" pg_dump -U hydra -Fc "$MULTICA_Z_DB_NAME" > "$BDIR/multica_z.dump" 2> "$BDIR/pg_dump.err"; then
  die "multica_z dump failed (see $BDIR/pg_dump.err); lane not touched"
fi
[ -s "$BDIR/multica_z.dump" ] || die "multica_z dump is empty; lane not touched"
log "backup: verifying checksums"
( cd "$BDIR" && sha256sum multica-be-env lane-runtime-env uploads-path.txt multica_z.dump > sha256sums.txt )
( cd "$BDIR" && sha256sum -c sha256sums.txt >/dev/null ) || die "backup checksum verification failed; lane not touched"

# Write override (mode 600)
umask 077
cat > "$OVERRIDE_FILE" <<EOF
# MMX pilot switch override (generated by switch-to-mmx.sh)
MULTICA_Z_DB_CONTAINER=$PG17_CONTAINER
MULTICA_Z_DB_HOST=$PG17_HOST
MULTICA_Z_DB_PORT=$PG17_PORT
MULTICA_Z_DB_NAME=$PG17_DB
MULTICA_Z_DB_ROLE=$PG17_ROLE
MULTICA_Z_DSN_TEMPLATE=postgres://$PG17_ROLE:PASSWORD@$PG17_HOST:$PG17_PORT/$PG17_DB?sslmode=disable
MULTICA_MMX_PG17_PW_FILE=$PG17_PW_FILE
MULTICA_Z_UPLOADS_DIR=$PG17_UPLOADS
EOF
chmod 600 "$OVERRIDE_FILE"
umask 022
# Put password into lane runtime env (mode 600, never echoed)
sed -i '/^MULTICA_Z_DB_PASSWORD=/d' "$LANE_RUNTIME_ENV"
printf 'MULTICA_Z_DB_PASSWORD=%s\n' "$PG17_PW" >> "$LANE_RUNTIME_ENV"
chmod 600 "$LANE_RUNTIME_ENV"

# Restart backend
systemctl --user restart "$MULTICA_BE_Z_UNIT"
sleep 2
curl -fsS "http://127.0.0.1:$MULTICA_BE_Z_PORT/healthz" >/dev/null || die "health check failed after switch"
log "switch complete; backup: $BDIR"
