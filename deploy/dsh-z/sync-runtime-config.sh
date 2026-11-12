#!/usr/bin/env bash
# sync-runtime-config.sh — repeatable, idempotent sync of lane-z runtime config.
#
# Regenerates $LANE_ROOT/data/multica-daemon-z.env from the committed lane
# config (deploy/dsh-z/dsh-z.env) plus the lane runtime secrets
# ($LANE_RUNTIME_ENV), including DSH_HOME (credential/home split, M-126/M-132)
# and the lane credential refs (M-103). Does not restart multica-daemon-z.
#
# Cross-lane guard: refuses unless LANE=z and all paths stay under
# /mnt/workspace/clouds/dsh-z. Secrets never leave the 0600 env file.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=dsh-z.env
source "$SCRIPT_DIR/dsh-z.env"

LANE_ROOT="${LANE_ROOT:?}"
LANE_HOME="${LANE_HOME:?}"
LANE_RUNTIME_ENV="${LANE_RUNTIME_ENV:?}"
DAEMON_ENV="$LANE_ROOT/data/multica-daemon-z.env"

# Cross-lane guard
[ "$LANE" = "z" ] || { echo "refuse: LANE=$LANE (expected z)" >&2; exit 1; }
case "$LANE_ROOT" in /mnt/workspace/clouds/dsh-z) ;; *) echo "refuse: LANE_ROOT=$LANE_ROOT" >&2; exit 1;; esac
case "$LANE_HOME" in /mnt/workspace/clouds/dsh-z/*) ;; *) echo "refuse: LANE_HOME=$LANE_HOME" >&2; exit 1;; esac

[ -f "$LANE_RUNTIME_ENV" ] || { echo "missing runtime env: $LANE_RUNTIME_ENV" >&2; exit 1; }
# shellcheck source=/dev/null
source "$LANE_RUNTIME_ENV"

# shellcheck source=credential-refs.sh
. "$SCRIPT_DIR/credential-refs.sh"

mkdir -p "$LANE_ROOT/data" "$LANE_HOME/.dsh"

# Optional subscription-store sync (DeepSeek, M-145): no-op until a key/store exists.
SUB_STORE=""
if [ -d "$LANE_HOME/profiles/web" ]; then
  SUB_STORE="$(find "$LANE_HOME/profiles/web" -maxdepth 4 -type f -iname '*subscription*' 2>/dev/null | head -1 || true)"
fi
if [ -n "$SUB_STORE" ]; then
  echo "sync: found subscription store candidate: $SUB_STORE"
  # Best-effort copy into .dsh if a matching target dir exists; never fail.
  cp -a "$SUB_STORE" "$LANE_HOME/.dsh/" 2>/dev/null || true
else
  echo "sync: no subscription store present (no-op; M-145 tracks the key)"
fi

# Regenerate daemon env atomically.
TMP="$LANE_ROOT/data/.multica-daemon-z.env.tmp"
umask 077
{
  echo "MULTICA_WORKSPACES_ROOT=$MULTICA_Z_WORKSPACES_ROOT"
  echo "MULTICA_WORKSPACE_ID=${MULTICA_Z_WORKSPACE_ID:-}"
  echo "DSH_HOME=$LANE_HOME"
  if [ -n "${LANE_AGENT_DEFAULT_MODEL:-}" ]; then
    echo "LANE_AGENT_DEFAULT_MODEL=$LANE_AGENT_DEFAULT_MODEL"
  fi
  if [ -n "${DSH_Z_DSH_BIN:-}" ] && [ -x "$DSH_Z_DSH_BIN" ]; then
    echo "MULTICA_DSH_PATH=$DSH_Z_DSH_BIN"
  fi
  if [ -n "${LANE_MOCT_BASE_URL:-}" ]; then
    echo "MOCT_BASE_URL=$LANE_MOCT_BASE_URL"
  fi
  emit_lane_credential_refs
} > "$TMP"
chmod 600 "$TMP"
mv -f "$TMP" "$DAEMON_ENV"
umask 022

# Verification output.
echo "sync: daemon env written -> $DAEMON_ENV"
echo "sync: mode=$(stat -c '%a' "$DAEMON_ENV")"
grep -q '^DSH_HOME=' "$DAEMON_ENV" && echo "sync: DSH_HOME present" || { echo "sync: DSH_HOME MISSING" >&2; exit 1; }
MANIFEST="$LANE_HOME/.dsh/profiles/multica/package.json"
if [ -f "$MANIFEST" ] && grep -q '@multica-ai/dsh-runtime' "$MANIFEST"; then
  echo "sync: profile manifest resolves (@multica-ai/dsh-runtime)"
else
  echo "sync: profile manifest NOT resolved: $MANIFEST" >&2
  exit 1
fi
if curl -fsS --max-time 5 "http://127.0.0.1:${MULTICA_BE_Z_PORT}/healthz" >/dev/null 2>&1; then
  echo "sync: healthz ok (port ${MULTICA_BE_Z_PORT})"
else
  echo "sync: healthz not reachable (port ${MULTICA_BE_Z_PORT})" >&2
fi
echo "sync: touched paths only under $LANE_ROOT"
echo "sync: done (idempotent; rerun safe)"