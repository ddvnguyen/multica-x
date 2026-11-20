#!/usr/bin/env bash
# scripts/build-mmx-pwa-artifact.sh — build + stage the lane-z MMX PWA
# artifact from a push tree (M-451). Shared by CI (frontend-build, push only)
# and the regression test, so the push-only path is tested, not duplicated.
#
# Inputs:  MMX_PWA_SHA   full commit sha (stamps sw.js via NEXT_PUBLIC_APP_VERSION)
#          MMX_PWA_WEB_DIR (default apps/web under the repo root)
#          MMX_PWA_SKIP_BUILD=1 skips `next build` and stages the existing
#            tree (regression-test mode with fixture trees)
#          MMX_PWA_STAGE_DIR (default $ROOT/mmx-pwa-stage) upload root
#          MMX_PWA_RELEASE_JSON (default $ROOT/release.json; CI's Stage
#            release.json step writes it there before this script runs)
# Output:  $STAGE/standalone + $STAGE/release.json — upload $STAGE as
#          mmx-pwa-$SHA (one directory path, so its contents are the
#          artifact root).
#
# Layout contract (M-457): autodeploy.sh verifies
# `standalone/apps/web/server.js`, the MMX manifest under
# `standalone/apps/web/.next/server/app/`, the SHA-stamped
# `standalone/apps/web/public/sw.js`, and a root `release.json` whose sha is
# the run sha. upload-artifact v4 roots a MULTI-path upload at the paths'
# common ancestor, so uploading `apps/web/.next/standalone` + `release.json`
# landed them at `apps/web/.next/standalone/...` and `release.json` and the
# lane refused every artifact. Stage the consumer's layout once, here, and
# upload the single directory.
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
# Stage the upload root in the layout autodeploy.sh reads (M-457). A single
# upload path is rooted at its own contents, so $STAGE/standalone and
# $STAGE/release.json become the artifact's top level.
STAND_ROOT="$WEB/.next/standalone"
[ -d "$STAND_ROOT" ] || { echo "no standalone tree at $STAND_ROOT" >&2; exit 1; }
RELEASE_JSON="${MMX_PWA_RELEASE_JSON:-$ROOT/release.json}"
[ -f "$RELEASE_JSON" ] || { echo "no release.json at $RELEASE_JSON" >&2; exit 1; }
# Node is already required by this job (next build runs under it) and it reads
# the JSON the same script wrote; no extra jq dependency on the runner.
REL_SHA="$(node -e 'const fs = require("node:fs"); const j = JSON.parse(fs.readFileSync(process.argv[1], "utf8")); process.stdout.write(String(j.sha || ""));' "$RELEASE_JSON")"
[ "$REL_SHA" = "$SHA" ] || {
  echo "release.json sha '$REL_SHA' != MMX_PWA_SHA '$SHA'; refusing to ship a mismatched release" >&2
  exit 1
}
STAGE="${MMX_PWA_STAGE_DIR:-$ROOT/mmx-pwa-stage}"
# The stage dir is recreated clean; never let a bad override point it at the
# build tree it copies from (or at the repo root's parent).
case "$STAGE" in
  ""|/|"${ROOT%/}"|"${STAND_ROOT%/}") echo "refusing unsafe MMX_PWA_STAGE_DIR '$STAGE'" >&2; exit 1 ;;
esac
rm -rf "$STAGE"
mkdir -p "$STAGE/standalone"
cp -a "$STAND_ROOT/." "$STAGE/standalone/"
cp -a "$RELEASE_JSON" "$STAGE/release.json"
echo "mmx-pwa artifact staged for $SHA at $STAGE (standalone/ + release.json)"
