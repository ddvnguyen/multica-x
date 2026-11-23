#!/usr/bin/env bash
# scripts/lane-z/rollback.sh — put lane z back on previous releases.
#
# Binaries only. It NEVER restores the database: `pg_restore` over a live DB
# discards every write since the dump, so a failed deploy flips the releases
# back, restarts the units, re-checks health, and leaves the dump path for a
# human to decide what the schema should be (M-151 condition 1).
# Usage:
#   rollback.sh                  the backend release recorded by the last
#                                auto-deploy
#   rollback.sh --to <sha|path>  an explicit backend release (sha prefix or
#                                directory)
#   rollback.sh --list           releases on disk, newest first
#
# Exit 0 when the lane answers on the target release, 1 otherwise.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The timer runs the installed copy under AUTODEPLOY_BIN_DIR, where
# install-units.sh also drops dsh-z.env; a repo checkout keeps the same file two
# levels up. Sourcing the copy means the host never depends on a task checkout
# that disappears.
ENV_FILE="$SCRIPT_DIR/dsh-z.env"
[ -f "$ENV_FILE" ] || ENV_FILE="$SCRIPT_DIR/../../deploy/dsh-z/dsh-z.env"
# shellcheck source=/dev/null
. "$ENV_FILE"
# shellcheck source=./lane-lib.sh
. "$SCRIPT_DIR/lane-lib.sh"

log() { printf '[rollback %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

RELEASES="$LANE_ROOT/multica-be/releases"
CURRENT="$LANE_ROOT/multica-be/current"
TARGET=""
EXPLICIT=0

case "${1:-}" in
  --list)
    ls -1dt "$RELEASES"/*/ 2>/dev/null | while read -r d; do
      marker=""
      [ "$(readlink -f "$d")" = "$(readlink -f "$CURRENT")" ] && marker=" (current)"
      printf '%s%s\n' "$(basename "$d")" "$marker"
    done
    exit 0
    ;;
  --to) TARGET="${2:-}"; EXPLICIT=1 ;;
  "") TARGET=""; EXPLICIT=0 ;;
  *) die "unknown argument: $1" ;;
esac

if [ -z "$TARGET" ]; then
  PREV_FILE="$LANE_ROOT/multica-be/previous-release"
  [ -f "$PREV_FILE" ] || die "no previous release recorded ($PREV_FILE)"
  TARGET="$(cat "$PREV_FILE")"
  [ -n "$TARGET" ] || die "previous-release is empty"
fi

# A bare sha (or sha prefix) names a release directory.
if [ ! -d "$TARGET" ]; then
  case "$TARGET" in
    /*) die "release directory does not exist: $TARGET" ;;
    *)
      MATCH="$(ls -1d "$RELEASES/$TARGET"* 2>/dev/null | head -1 || true)"
      [ -n "$MATCH" ] || die "no release matching '$TARGET' under $RELEASES"
      TARGET="$MATCH"
      ;;
  esac
fi
TARGET="$(readlink -f "$TARGET")"
[ -x "$TARGET/bin/server" ] || die "$TARGET has no bin/server"
[ -f "$TARGET/.complete" ] || die "$TARGET is not a complete release (no .complete)"
TARGET_SHA="$(jq -r '.sha // empty' "$TARGET/release.json")"
[ -n "$TARGET_SHA" ] && [ "$TARGET_SHA" != "null" ] || die "$TARGET has no sha in release.json"
BACKEND_ALREADY=0
[ "$TARGET" = "$(readlink -f "$CURRENT")" ] && BACKEND_ALREADY=1
if [ "$BACKEND_ALREADY" = 1 ]; then
  log "backend already on $(basename "$TARGET"); verifying health without restart"
else
  log "switching $(readlink -f "$CURRENT") -> $TARGET"
  # Flips `current` and repoints the unit at it; a pinned unit would otherwise
  # restart the release we are trying to leave.
  render_backend_unit "$TARGET" || die "could not point $MULTICA_BE_Z_UNIT at $TARGET"
  [ "$(readlink -f "$CURRENT")" = "$TARGET" ] || die "current does not resolve to $TARGET"
  systemctl --user restart "$MULTICA_BE_Z_UNIT"
fi

SHA="$TARGET_SHA"
wait_http "http://127.0.0.1:$MULTICA_BE_Z_PORT/healthz" 120 '"status":"ok"' \
  || die "multica-be z /healthz did not answer ok after the rollback"
# The lane web binds the podman bridge (10.88.0.1 on lane z), not loopback, so
# a hardcoded 127.0.0.1:3180 curl can never pass there and every rollback
# printed ROLLBACK FAILED over a correctly switched backend (M-458).
commit="$(curl -fsS --max-time 5 "$(resolve_lane_web_url)/multica/health" | jq -r '.commit // empty' || true)"
commit_matches "$commit" "$SHA" || die "/multica/health reports '$commit', expected '$SHA'"
log "lane z backend is on ${SHA:0:9} ($(jq -r '.version // "unknown"' "$TARGET/release.json")); the database was NOT touched"
log "lane z is on ${SHA:0:9}; the database was NOT touched"
