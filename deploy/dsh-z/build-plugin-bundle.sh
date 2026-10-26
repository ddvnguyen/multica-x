#!/usr/bin/env bash
# deploy/dsh-z/build-plugin-bundle.sh — package @multica/dsh-plugin for the lane
# web profile.
#
# deploy/dsh-z/ does not build the monorepo (a pnpm workspace install is heavier
# than any lane phase should pay), so the plugin ships the way the runtime does
# in phase 9b: as a directory bundle on the lane disk that `dsh plugin --profile
# web add` consumes. This script produces that bundle from any multica-x
# checkout that already has node_modules installed: it copies the built lib/
# into DSH_Z_PLUGIN_BUNDLE and rewrites the package manifest so the bundle only
# declares what the built code needs at runtime (ws — the client half already
# bundles React 19 for the browser).
#
# Run once per plugin version: bash deploy/dsh-z/build-plugin-bundle.sh <checkout-root>

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/dsh-z.env"
die() { echo "ERROR: $*" >&2; exit 1; }

SRC="${1:-}"
[ -n "$SRC" ] || die "usage: $0 <multica-x checkout root> [base-version]"
for f in package.json cordis.patch.yml FORK.md README.md; do
  [ -f "$SRC/packages/dsh-plugin/$f" ] || die "missing packages/dsh-plugin/$f in $SRC"
done
[ -f "$SRC/packages/dsh-plugin/lib/index.js" ] || die "packages/dsh-plugin is not built in $SRC (run: pnpm --filter @multica/dsh-plugin build)"
[ -f "$SRC/node_modules/ws/package.json" ] || die "$SRC has no pnpm install; run pnpm install in the checkout first"
[ -n "${DSH_Z_PLUGIN_BUNDLE:-}" ] || die "set DSH_Z_PLUGIN_BUNDLE in dsh-z.env"

VER="$(node -p "require('$SRC/packages/dsh-plugin/package.json').version")"
if [ -n "${2:-}" ]; then VER="$2"; fi
rm -rf "$DSH_Z_PLUGIN_BUNDLE"
mkdir -p "$DSH_Z_PLUGIN_BUNDLE"

cp -a "$SRC/packages/dsh-plugin/lib" "$DSH_Z_PLUGIN_BUNDLE/lib"

# tsc compiles with bundler-style resolution, so the repo lib keeps ESM
# specifiers without the .js extension ("./proxy"). Bundler-pipeline consumers
# resolve that; the dsh plugin host imports lib/*.js as bare ESM and cannot,
# which is exactly "Cannot find module '<bundle>/lib/proxy'" in the web boot
# log. Rewrite the (verified: only) extensionless relative specifier.
if grep -q 'from "\./proxy"' "$DSH_Z_PLUGIN_BUNDLE/lib/index.js"; then
  sed -i 's|from "\./proxy"|from "./proxy.js"|g' "$DSH_Z_PLUGIN_BUNDLE/lib/index.js"
fi
left_over="$(grep -ho 'from "\./[a-zA-Z0-9/._-]*"' "$DSH_Z_PLUGIN_BUNDLE/lib/"*.js | grep -v '\.\(js\|mjs\|json\|css\|cjs\)"$' | sort -u || true)"
[ -z "$left_over" ] || die "unrewritten extensionless relative imports in bundle lib/: $left_over"

cp "$SRC/packages/dsh-plugin/cordis.patch.yml" \
   "$SRC/packages/dsh-plugin/FORK.md" \
   "$SRC/packages/dsh-plugin/README.md" \
   "$DSH_Z_PLUGIN_BUNDLE/"

# Runtime manifest only: workspace devDeps ("@multica/core": "workspace:*",
# ...) are compiled into lib/ and must not be resolved again by the profile's
# own pnpm install — there is no workspace in the profile to resolve them
# from. The same rule drops the build/test tooling.
node - "$SRC/packages/dsh-plugin/package.json" "$DSH_Z_PLUGIN_BUNDLE/package.json" "$VER" <<'EOF'
const fs = require("node:fs");
const [src, out, ver] = process.argv.slice(2);
const p = JSON.parse(fs.readFileSync(src, "utf8"));
const bundle = {
  name: p.name,
  version: `${ver}+lanez`,
  private: p.private,
  description: p.description,
  type: p.type,
  license: p.license,
  main: p.main,
  types: p.types,
  files: p.files,
  exports: p.exports,
  dsh: p.dsh,
  dependencies: p.dependencies,
};
fs.writeFileSync(out, JSON.stringify(bundle, null, 2) + "\n");
EOF

# The profile installs this bundle by symlink (link:), and Node resolves
# imports from the link TARGET's real path — so the 'ws' lib/proxy.js depends
# on has to exist inside the bundle itself; nothing above the bundle on disk
# has it. Vendor it from packages/dsh-plugin's own package resolution, not the
# checkout root: the root cache can carry an older hoisted copy (ws@7 here —
# export shape has no WebSocketServer and it breaks the host half at startup),
# while packages/dsh-plugin pins ws ^8.18.3 through its own link.
mkdir -p "$DSH_Z_PLUGIN_BUNDLE/node_modules"
if [ ! -d "$SRC/packages/dsh-plugin/node_modules/ws" ]; then
  die "$SRC/packages/dsh-plugin/node_modules/ws missing — run pnpm install in $SRC first"
fi
rm -rf "$DSH_Z_PLUGIN_BUNDLE/node_modules/ws"
# -L: the ws path in a pnpm tree is a symlink into .pnpm; the vendor copy must
# materialize real files or the bundle ships a dangling link.
cp -aL "$SRC/packages/dsh-plugin/node_modules/ws" "$DSH_Z_PLUGIN_BUNDLE/node_modules/ws"

# The host half imports `ws` — bundled copies must not rely on bare-name
# resolution for it, for two reasons both seen live: (1) the package ships
# CommonJS only and Node's ESM named-export detection does not surface the
# constructors ("does not provide an export named 'WebSocket'"); (2) the dsh
# host resolves bare names with its own conditions, where the ws "browser"
# export can win and that build is a bare WebSocket class with no
# WebSocketServer ("WebSocketServer is not a constructor"). Vendor ws into the
# bundle AND route the import at the vendored CJS entry by explicit relative
# path: file imports get no condition resolution, and the CJS interop default
# is module.exports with the constructors the host half expects.
if [ ! -d "$SRC/packages/dsh-plugin/node_modules/ws" ]; then
  die "$SRC/packages/dsh-plugin/node_modules/ws missing — run pnpm install in $SRC first"
fi
mkdir -p "$DSH_Z_PLUGIN_BUNDLE/node_modules"
rm -rf "$DSH_Z_PLUGIN_BUNDLE/node_modules/ws"
# -L: the ws path in a pnpm tree is a symlink into .pnpm; the vendor copy must
# materialize real files or the bundle ships a dangling link.
cp -aL "$SRC/packages/dsh-plugin/node_modules/ws" "$DSH_Z_PLUGIN_BUNDLE/node_modules/ws"
node -e 'require(process.argv[1])' "$DSH_Z_PLUGIN_BUNDLE/node_modules/ws/package.json" >/dev/null \
  || die "vendored ws is unusable"
if ! node -p "require('$DSH_Z_PLUGIN_BUNDLE/node_modules/ws/package.json').version.startsWith('8.')"; then
  die "vendored ws is not v8 — the host half is compiled against the ws v8 export shape"
fi

if grep -q 'import { WebSocket, WebSocketServer } from "ws";' "$DSH_Z_PLUGIN_BUNDLE/lib/proxy.js"; then
  sed -i 's|import { WebSocket, WebSocketServer } from "ws";|import wsModule from "../node_modules/ws/index.js";\nconst { WebSocket, WebSocketServer } = wsModule;|' "$DSH_Z_PLUGIN_BUNDLE/lib/proxy.js"
fi
left_over="$(grep -c 'import { WebSocket, WebSocketServer } from "ws"' "$DSH_Z_PLUGIN_BUNDLE/lib/proxy.js" || true)"
[ "$left_over" = "0" ] || die "unrewritten ws import in bundle lib/proxy.js"

echo "wrote $DSH_Z_PLUGIN_BUNDLE (built from $SRC @ $(git -C "$SRC" rev-parse HEAD 2>/dev/null || echo unknown))"
