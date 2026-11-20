#!/usr/bin/env bash
# scripts/build-mmx-pwa-artifact.sh — build + stage the lane-z MMX PWA
# artifact from a push tree (M-451). Shared by CI (frontend-build, push only)
# and the regression test, so the push-only path is tested, not duplicated.
#
# Inputs:  MMX_PWA_SHA   full commit sha (stamps sw.js via NEXT_PUBLIC_APP_VERSION)
#          MMX_PWA_WEB_DIR (default apps/web under the repo root)
#          MMX_PWA_SKIP_BUILD=1 skips `next build` and stages the existing
#            tree (regression-test mode with fixture trees)
# Output:  $WEB/.next/standalone + release.json (upload these as mmx-pwa-$SHA)
#
# Brand gate: the prerendered manifest route body must carry the MMX id and
# name. A raw 'mmx-install' grep over .next is NOT a brand signal — server
# chunks embed both brands' literals as source; only the manifest route
# output proves which brand the build baked.
#
# Fails when:
#   * sw.js has no __MMX_PWA_VERSION__ placeholder (unversioned SW refused),
#   * the manifest body is not the MMX brand (not the MMX brand build — e.g.
#     the default web-dist tree was uploaded under the wrong name).
set -euo pipefail

SHA="${MMX_PWA_SHA:?set MMX_PWA_SHA to the full commit sha}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB="${MMX_PWA_WEB_DIR:-$ROOT/apps/web}"

if [ -z "${MMX_PWA_SKIP_BUILD:-}" ]; then
  export MMX_PWA=1
  export NEXT_PUBLIC_APP_VERSION="$SHA"
  export STANDALONE=true
  ( cd "$WEB" && pnpm next build )
fi

STAND="$WEB/.next/standalone/apps/web"
mkdir -p "$STAND/.next"
cp -a "$WEB/public" "$STAND/public"
cp -a "$WEB/.next/static" "$STAND/.next/static"
if grep -q __MMX_PWA_VERSION__ "$STAND/public/sw.js"; then
  sed -i "s/__MMX_PWA_VERSION__/${SHA}/g" "$STAND/public/sw.js"
else
  echo "sw.js has no version placeholder; refusing to ship an unversioned service worker" >&2
  exit 1
fi
MANIFEST_BODY="$STAND/.next/server/app/manifest.webmanifest.body"
if [ -f "$MANIFEST_BODY" ] \
  && grep -q '"id":"/?mmx-install"' "$MANIFEST_BODY" \
  && grep -q '"name":"MMX"' "$MANIFEST_BODY"; then
  :
else
  echo "MMX brand manifest missing; the artifact is not the MMX build" >&2
  exit 1
fi
echo "mmx-pwa artifact staged for $SHA"
