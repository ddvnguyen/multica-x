#!/usr/bin/env bash
# scripts/build-mmx-pwa.sh — build the MMX PWA (installable apps/web on its own
# port, same backend). Lane-z recipe for M-390; main-stack rollout stays with S5.
#
# Usage (from repo root):
#   MMX_PWA_PORT=3190 REMOTE_API_URL=http://127.0.0.1:4390 \
#     scripts/build-mmx-pwa.sh
#
# What it does:
#   1. next build with MMX_PWA=1 (+ NEXT_PUBLIC_APP_VERSION for the SW cache).
#   2. stamps the standalone copy of sw.js with the build version.
#   3. writes a standalone start recipe (no files outside the repo).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB="$ROOT/apps/web"

PORT="${MMX_PWA_PORT:?set MMX_PWA_PORT (lane z: 3190)}"
REMOTE="${REMOTE_API_URL:?set REMOTE_API_URL (lane z: http://127.0.0.1:4390)}"
VERSION="${NEXT_PUBLIC_APP_VERSION:-dev}"

export MMX_PWA=1
export NEXT_PUBLIC_APP_VERSION="$VERSION"
export REMOTE_API_URL="$REMOTE"
export STANDALONE=true

( cd "$WEB" && pnpm next build )

# Standalone omits public + .next/static by design (Next docs: copy them in).
# Then stamp the release copy of sw.js, never the source tree, so rebuilds
# stay idempotent.
STAND="$WEB/.next/standalone/apps/web"
mkdir -p "$STAND/.next"
cp -a "$WEB/public" "$STAND/public"
cp -a "$WEB/.next/static" "$STAND/.next/static"
SW="$STAND/public/sw.js"
if [ -f "$SW" ] && grep -q "__MMX_PWA_VERSION__" "$SW"; then
  sed -i "s/__MMX_PWA_VERSION__/${VERSION}/g" "$SW"
fi

cat <<EOF
MMX PWA built: $WEB/.next/standalone (version $VERSION)
Start: MMX_PWA=1 REMOTE_API_URL=$REMOTE PORT=$PORT pnpm --filter @multica/web start
Health: curl http://127.0.0.1:$PORT/manifest.webmanifest
Portal: https://portal.ddvnguyen.com/p/$PORT/ (after ports.yaml entry)
EOF
