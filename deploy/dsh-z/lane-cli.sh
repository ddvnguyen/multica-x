#!/usr/bin/env bash
# lane-cli.sh — run the multica CLI against the lane (multica-be z), never the
# live control plane.
#
# Why the strip matters: server URL resolution order is --server-url flag >
# MULTICA_SERVER_URL env > profile config (server/cmd/multica/cmd_agent.go).
# Inside a daemon-managed task the environment carries MULTICA_SERVER_URL,
# MULTICA_TOKEN and MULTICA_WORKSPACE_ID pointing at the LIVE control plane —
# without the strip, any flagless lane call silently targets the live server
# with live credentials. The lane profile carries server_url, token and
# workspace_id itself, so every MULTICA_* variable can go.
#
# Usage: deploy/dsh-z/lane-cli.sh [--profile z] <command>…
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=dsh-z.env
source "$SCRIPT_DIR/dsh-z.env"

LANE_CLI="$LANE_ROOT/multica-be/current/bin/multica"
if [ ! -x "$LANE_CLI" ]; then
  echo "lane CLI not found at $LANE_CLI — run deploy/dsh-z/deploy.sh first" >&2
  exit 1
fi

strip=("-u" "HOME")
while IFS= read -r name; do
  strip+=("-u" "$name")
done < <(env | grep -oE '^MULTICA_[A-Z0-9_]+' || true)

# Default to the lane profile unless the caller chose one.
profile_flag=(--profile "$MULTICA_Z_PROFILE")
for arg in "$@"; do
  case "$arg" in
    --profile|--profile=*) profile_flag=(); break ;;
  esac
done

cd "$LANE_ROOT"
exec env ${strip+"${strip[@]}"} HOME="$LANE_HOME" "$LANE_CLI" \
  ${profile_flag+"${profile_flag[@]}"} "$@"
