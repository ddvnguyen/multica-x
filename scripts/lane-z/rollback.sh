#!/usr/bin/env bash
# scripts/lane-z/rollback.sh — put lane z back on previous releases.
#
# Binaries only. It NEVER restores the database: `pg_restore` over a live DB
# discards every write since the dump, so a failed deploy flips the releases
# back, restarts the units, re-checks health, and leaves the dump path for a
# human to decide what the schema should be (M-151 condition 1).
# Usage:
#   rollback.sh                  the backend release recorded by the last
#                                auto-deploy; the PWA follows to the release
#                                named by the same SHA
#   rollback.sh --to <sha|path>  an explicit backend release (sha prefix or
#                                directory); the PWA follows to
#                                mmx-pwa/releases/<that SHA>, env rewritten
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
commit="$(curl -fsS --max-time 5 http://127.0.0.1:3180/multica/health | jq -r '.commit // empty' || true)"
commit_matches "$commit" "$SHA" || die "/multica/health reports '$commit', expected '$SHA'"
log "lane z backend is on ${SHA:0:9} ($(jq -r '.version // "unknown"' "$TARGET/release.json")); the database was NOT touched"

# M-451: the MMX PWA release tracks the backend SHA one-to-one, so it rolls
# back to the release NAMED BY THE SAME SHA — not the last PWA marker, which
# may belong to a different (failed) deploy. An explicit --to names the PWA
# release directly; the default pairs mmx-pwa/releases/$SHA with the backend
# target. Env is rewritten (NEXT_PUBLIC_APP_VERSION follows the target SHA)
# before the restart so a rolled-back PWA never serves a stale version stamp.
# M-455: on the default (non-explicit) path, when mmx-pwa/releases/$SHA is
# absent from disk — the first post-merge deploy, where the backend previous
# release predates the PWA install — fall back to the release named in
# mmx-pwa/previous-release when it is a complete release. Explicit --to keeps
# exact SHA pairing (no fallback). A missing PWA release with no usable
# marker stays a loud warning, never a failure (the backend is healthy).
MMX_REL="$LANE_ROOT/mmx-pwa/releases/$SHA"
PWA_SHA="$SHA"
if [ ! -d "$MMX_REL" ] && [ "$EXPLICIT" = 0 ]; then
  MARKER_FILE="$LANE_ROOT/mmx-pwa/previous-release"
  MARKER_CONTENT=""
  if [ -f "$MARKER_FILE" ]; then
    MARKER_CONTENT="$(tr -d '\r\n' < "$MARKER_FILE" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true)"
  fi
  FALLBACK_CAND=""
  if [ -n "${MARKER_CONTENT:-}" ]; then
    case "$MARKER_CONTENT" in
      /*) FALLBACK_CAND="$MARKER_CONTENT" ;;
      *) FALLBACK_CAND="$LANE_ROOT/mmx-pwa/releases/$MARKER_CONTENT" ;;
    esac
  fi
  FALLBACK_SHA=""
  if [ -n "$FALLBACK_CAND" ] && [ -d "$FALLBACK_CAND" ] \
    && [ -f "$FALLBACK_CAND/.complete" ] \
    && [ -f "$FALLBACK_CAND/apps/web/server.js" ]; then
    if [ -f "$FALLBACK_CAND/release.json" ]; then
      FALLBACK_SHA="$(jq -r '.sha // empty' "$FALLBACK_CAND/release.json" 2>/dev/null || true)"
    fi
    if [ -z "$FALLBACK_SHA" ]; then
      FALLBACK_BASE="$(basename "$FALLBACK_CAND")"
      case "$FALLBACK_BASE" in
        *[!0-9a-fA-F]*|"") FALLBACK_SHA="" ;;
        *) [ "${#FALLBACK_BASE}" -ge 7 ] && FALLBACK_SHA="$FALLBACK_BASE" || FALLBACK_SHA="" ;;
      esac
    fi
    if [ -z "$FALLBACK_SHA" ] && [ -f "$FALLBACK_CAND/apps/web/public/sw.js" ]; then
      FALLBACK_SHA="$(grep -oE '[0-9a-f]{40}' "$FALLBACK_CAND/apps/web/public/sw.js" 2>/dev/null | head -1 || true)"
    fi
  else
    FALLBACK_CAND=""
  fi
  if [ -n "$FALLBACK_CAND" ] && [ -n "$FALLBACK_SHA" ]; then
    log "mmx-pwa release for ${SHA:0:9} absent; falling back to previous-release ${FALLBACK_SHA:0:9} ($FALLBACK_CAND)"
    MMX_REL="$FALLBACK_CAND"
    PWA_SHA="$FALLBACK_SHA"
  else
    log "WARNING: no mmx-pwa release for ${SHA:0:9} on disk and no usable mmx-pwa/previous-release marker; leaving mmx-pwa z on $(readlink -f "$LANE_ROOT/mmx-pwa/current" 2>/dev/null || printf unknown) (backend is healthy on ${SHA:0:9})"
    MMX_REL=""
  fi
elif [ ! -d "$MMX_REL" ]; then
  log "WARNING: no mmx-pwa release for ${SHA:0:9} on disk; leaving mmx-pwa z on $(readlink -f "$LANE_ROOT/mmx-pwa/current" 2>/dev/null || printf unknown) (backend is healthy on ${SHA:0:9})"
  MMX_REL=""
fi
if [ -n "$MMX_REL" ] && [ -d "$MMX_REL" ]; then
  [ -f "$MMX_REL/.complete" ] || die "mmx-pwa release $MMX_REL is not complete (no .complete)"
  [ -f "$MMX_REL/apps/web/server.js" ] || die "mmx-pwa release $MMX_REL has no apps/web/server.js"
  MMX_REL="$(readlink -f "$MMX_REL")"
  MMX_RESTARTED=0
  if [ "$MMX_REL" != "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" ]; then
    log "switching mmx-pwa $(readlink -f "$LANE_ROOT/mmx-pwa/current") -> $MMX_REL"
    ln -sfn "$MMX_REL" "$LANE_ROOT/mmx-pwa/current"
    [ "$(readlink -f "$LANE_ROOT/mmx-pwa/current")" = "$MMX_REL" ] || die "mmx-pwa current does not resolve to $MMX_REL"
    MMX_RESTARTED=1
  else
    log "mmx-pwa already on $(basename "$MMX_REL"); rewriting env and re-verifying"
  fi
  # shellcheck source=/dev/null
  if [ -f "$LANE_RUNTIME_ENV" ]; then . "$LANE_RUNTIME_ENV"; fi
  [ -n "${MULTICA_Z_DEV_CODE:-}" ] || die "no MULTICA_Z_DEV_CODE in $LANE_RUNTIME_ENV"
  umask 077
  {
    echo "PORT=${MMX_PWA_Z_PORT:?MMX_PWA_Z_PORT is not set}"
    echo "HOSTNAME=127.0.0.1"
    echo "MMX_BIND_HOST=127.0.0.1"
    echo "REMOTE_API_URL=http://127.0.0.1:${MULTICA_BE_Z_PORT:?MULTICA_BE_Z_PORT is not set}"
    echo "NEXT_PUBLIC_APP_VERSION=$PWA_SHA"
    echo "MMX_AUTH_BYPASS=${MMX_AUTH_BYPASS:-1}"
    echo "MULTICA_DEV_VERIFICATION_CODE=${MULTICA_Z_DEV_CODE:?MULTICA_Z_DEV_CODE is not set}"
  } > "$LANE_ROOT/mmx-pwa/env"
  umask 022
  systemctl --user restart "${MMX_PWA_Z_UNIT:?MMX_PWA_Z_UNIT is not set}"
  wait_http "http://127.0.0.1:$MMX_PWA_Z_PORT/manifest.webmanifest" 120 'MMX' \
    || die "mmx-pwa z manifest did not come back after the rollback"
  MMX_MANIFEST="$(curl -fsS --max-time 10 "http://127.0.0.1:$MMX_PWA_Z_PORT/manifest.webmanifest" || true)"
  printf '%s' "$MMX_MANIFEST" | grep -q '"name":"MMX"' \
    || die "mmx-pwa z manifest is not the MMX brand after the rollback"
  curl -fsS --max-time 10 "http://127.0.0.1:$MMX_PWA_Z_PORT/sw.js" | grep -q "$PWA_SHA" \
    || die "mmx-pwa z sw.js is not stamped with $PWA_SHA after the rollback"
  log "mmx-pwa z is on ${PWA_SHA:0:9} (brand MMX, sw.js stamped)"
fi
log "lane z is on ${SHA:0:9}; the database was NOT touched"
