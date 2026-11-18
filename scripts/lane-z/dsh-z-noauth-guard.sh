#!/usr/bin/env bash
# dsh-z-noauth-guard.sh — keep DSH-Z web reachable without per-request token
# on lane z (M-406). Reapplies the M-405 noAuth profile fix when overwritten
# (redeploy / `dsh plugin --profile web install`) and runs the lane smoke
# checks. Lane z only.
#
# Commands:
#   ensure   Verify behavior (probe). Reapply supported fix files if missing;
#            if probe fails after/without reapply, report NEEDS_RESTART (10).
#   reapply-files  Idempotent file-level reapply only (no probe). For use
#            before a planned web restart (plugin update path).
#   smoke    Run smoke checks and print a one-line summary. Exit non-zero on
#            gate failure.
#   simulate-copy <dest-dir>
#            Copy the live profile node_modules packages to dest-dir and, with
#            --break, revert the patched client-connection marker so ensure can
#            be proven to reapply on the copy (no live profile mutation).
#
# Exit codes: 0 ok, 10 reapplied/needs restart (ensure), 20 unsupported/unsafe,
# 30 smoke failed, 1 usage/other error.

set -euo pipefail

log() { printf '[dsh-z-noauth %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*" >&2; }

usage() { sed -n '2,35p' "$0" | sed 's/^# \{0,1\}//'; exit 1; }

# Defaults (overridable by env/flags)
LANE_ROOT="${LANE_ROOT:-/mnt/workspace/clouds/dsh-z}"
LANE_HOME="${LANE_HOME:-$LANE_ROOT/data/home}"
FIX_ROOT="${DSH_Z_NOAUTH_FIX_ROOT:-$LANE_ROOT/data/fixes/m406}"
PROFILE_WEB="${DSH_Z_PROFILE_WEB:-$LANE_HOME/profiles/web}"
WEB_URL="${DSH_Z_WEB_URL:-}"
GATEWAY_URL="${DSH_Z_GATEWAY_URL:-http://127.0.0.1:3738}"
CURL_MAX="${DSH_Z_SMOKE_CURL_MAX:-8}"

# Resolve web base URL like autodeploy if not provided.
resolve_web_url() {
  local unit_file="" host port unit="${DSH_Z_WEB_UNIT:-dsh-z.service}"
  if [ -n "$unit" ]; then
    for c in "$HOME/.config/systemd/user/$unit" "/etc/systemd/user/$unit" "$HOME/.local/share/systemd/user/$unit"; do
      [ -f "$c" ] && { unit_file="$c"; break; }
    done
  fi
  host="$(sed -n 's/^ExecStart=.*--host \([^ ]*\).*/\1/p' "$unit_file" 2>/dev/null | head -1)"
  [ -n "$host" ] || host="10.88.0.1"
  port="$(sed -n 's/^ExecStart=.*--port \([^ ]*\).*/\1/p' "$unit_file" 2>/dev/null | head -1)"
  [ -n "$port" ] || port="3180"
  printf 'http://%s:%s' "$host" "$port"
}

http_code() { # url
  curl -sS -o /dev/null --max-time "$CURL_MAX" -w '%{http_code}' "$1" 2>/dev/null || printf '000'
}

pkg_dir() { # profile_web pkg
  printf '%s/node_modules/@deepseek-ai/%s' "$1" "$2"
}

pkg_version() { # pkg_root (resolves symlink target for package.json)
  local root="$1" p
  p="$root/package.json"
  [ -f "$p" ] || { printf 'missing'; return 0; }
  jq -r '.version // "unknown"' "$p" 2>/dev/null || printf 'unknown'
}

manifest_pkg_mode() { # manifest pkg
  jq -r --arg p "$2" '.packages[$p].mode // "patched_dir"' "$1" 2>/dev/null || printf 'patched_dir'
}

expected_current_auth_token_target() { # from profile pkg location
  # live state: ../../../../../../current/packages/host/authentication-token
  printf '%s' "../../../../../../current/packages/host/authentication-token"
}

# Marker proving client-connection fork.noAuth is honored in runtime lib.
has_noauth_marker() {
  local f="$1/lib/index.js"
  [ -f "$f" ] || return 1
  grep -q 'this.noAuth = noAuth' "$f" \
    && grep -q 'fork: z.object({ noAuth: z.boolean().default(false)' "$f" \
    && grep -q 'config?.fork?.noAuth === true' "$f"
}

find_fix_version() { # ver -> prints fix dir for supported version
  local ver d
  ver="$1"
  d="$FIX_ROOT/v$ver"
  [ -d "$d" ] && [ -f "$d/manifest.json" ] && { printf '%s' "$d"; return 0; }
  return 1
}

smoke_check() { # sets/prints SMOKE_* vars via stdout line
  local web="$1" gw="$2"
  local direct_root direct_jira gw_jira t0 t1
  t0=$(date +%s%3N 2>/dev/null || date +%s)
  direct_root="$(http_code "$web/")"
  direct_jira="$(http_code "$web/plugins/dsh-claude/jira/status")"
  gw_jira="$(http_code "$gw/p/3180/plugins/dsh-claude/jira/status")"
  t1=$(date +%s%3N 2>/dev/null || date +%s)
  # Gate per Architect/M-406: direct root 200 AND gateway jira/status 200.
  # Direct jira/status is informational (peer/trust path differs from portal).
  local gate=pass
  [ "$direct_root" = "200" ] || gate=fail
  [ "$gw_jira" = "200" ] || gate=fail
  if [ "${DSH_Z_FORCE_SMOKE_FAIL:-0}" = "1" ]; then gate=fail; direct_root="${direct_root}(forced-fail)"; fi
  printf 'gate=%s direct_root=%s gw_jira=%s direct_jira=%s web=%s gw=%s ms=%s\n' \
    "$gate" "$direct_root" "$gw_jira" "$direct_jira" "$web" "$gw" "$(( ${t1#0} - ${t0#0} ))" 2>/dev/null \
    || printf 'gate=%s direct_root=%s gw_jira=%s direct_jira=%s web=%s gw=%s\n' \
       "$gate" "$direct_root" "$gw_jira" "$direct_jira" "$web" "$gw"
  [ "$gate" = "pass" ]
}

backup_pkg() { # pkg_root label
  local root="$1" label="$2" ts bdir
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  bdir="${root}.bak-m406-${label}-${ts}"
  cp -a "$root" "$bdir"
  log "backup: $bdir"
}

reapply_pkg() { # pkg_name profile_web fix_dir
  local pkg="$1" pweb="$2" fdir="$3" target src mode manifest
  target="$(pkg_dir "$pweb" "$pkg")"
  src="$fdir/$pkg"
  manifest="$fdir/manifest.json"
  mode="$(manifest_pkg_mode "$manifest" "$pkg")"
  [ -e "$target" ] || [ -L "$target" ] || { log "missing target package $target"; return 20; }

  if [ "$mode" = "symlink_current" ]; then
    local want cur_resolved cur_ver src_ver
    want="$(expected_current_auth_token_target)"
    # already correct symlink?
    if [ -L "$target" ] && [ "$(readlink "$target" 2>/dev/null || true)" = "$want" ] && [ -f "$target/package.json" ]; then
      log "$pkg already aligned (symlink -> $want)"
      return 0
    fi
    # if target already has correct version/content (e.g. dereferenced sim copy), accept
    cur_ver="$(pkg_version "$target")"
    src_ver="$(pkg_version "$src")"
    if [ "$cur_ver" != "missing" ] && [ "$cur_ver" = "$src_ver" ] && [ -d "$target/lib" ]; then
      # try to establish canonical symlink when resolvable; else keep valid content
      backup_pkg "$target" "reapply"
      rm -rf "$target"
      ln -s "$want" "$target"
      if [ -f "$target/package.json" ]; then
        log "aligned $pkg as symlink -> $want"
        return 0
      fi
      # symlink not resolvable from this location (e.g. sim copy): restore content
      rm -rf "$target"
      mkdir -p "$target"
      cp -R -L "$src/lib" "$target/lib" 2>/dev/null || true
      [ -f "$src/package.json" ] && cp -L "$src/package.json" "$target/package.json" || true
      [ -d "$src/locale" ] && cp -R -L "$src/locale" "$target/locale" || true
      log "$pkg aligned as content copy (symlink target not resolvable here)"
      return 0
    fi
    backup_pkg "$target" "reapply"
    rm -rf "$target"
    ln -s "$want" "$target"
    if [ ! -f "$target/package.json" ]; then
      # fallback to content from fix assets if available
      rm -rf "$target"; mkdir -p "$target"
      if [ -d "$src/lib" ]; then cp -R -L "$src/lib" "$target/lib"; fi
      [ -f "$src/package.json" ] && cp -L "$src/package.json" "$target/package.json" || true
      [ -d "$src/locale" ] && cp -R -L "$src/locale" "$target/locale" || true
      if [ ! -f "$target/package.json" ]; then
        log "symlink alignment for $pkg could not be established"; return 20
      fi
      log "$pkg aligned as content copy (symlink unresolved)"
      return 0
    fi
    log "aligned $pkg as symlink -> $want"
    return 0
  fi

  # patched_dir (or default): restore known-good lib/package bits
  [ -d "$src/lib" ] || { log "missing fix source $src/lib"; return 20; }
  # if target is a symlink, replace with real dir copy of current content then patch
  if [ -L "$target" ]; then
    local resolved
    resolved="$(readlink -f "$target" 2>/dev/null || true)"
    backup_pkg "$target" "reapply"
    rm -f "$target"
    if [ -n "$resolved" ] && [ -d "$resolved" ]; then
      cp -a "$resolved" "$target"
    else
      mkdir -p "$target"
    fi
  else
    backup_pkg "$target" "reapply"
  fi
  rm -rf "$target/lib"
  cp -a "$src/lib" "$target/lib"
  [ -f "$src/package.json" ] && cp -a "$src/package.json" "$target/package.json" || true
  if [ -d "$src/locale" ]; then rm -rf "$target/locale"; cp -a "$src/locale" "$target/locale"; fi
  log "reapplied $pkg from $src"
}

cmd_ensure() {
  local web="${WEB_URL:-$(resolve_web_url)}" gw="$GATEWAY_URL" pweb="$PROFILE_WEB"
  local cc at ccv atv fix changed=0

  cc="$(pkg_dir "$pweb" dsh-client-connection)"
  at="$(pkg_dir "$pweb" dsh-host-authentication-token)"
  ccv="$(pkg_version "$cc")"; atv="$(pkg_version "$at")"

  # Version pin: fail loudly if not a supported/patched version.
  fix="$(find_fix_version "$ccv" || true)"
  if [ -z "$fix" ] || [ "$atv" != "$ccv" ] || [ ! -d "$(printf '%s/dsh-host-authentication-token' "$fix")" ]; then
    log "UNSUPPORTED: dsh-client-connection=$ccv dsh-host-authentication-token=$atv fix=${fix:-none}"
    log "refusing to patch blindly (version mismatch or no durable fix assets)"
    return 20
  fi

  # File-level ensure (idempotent): keep supported packages aligned/patched.
  if ! has_noauth_marker "$cc"; then
    log "noAuth marker missing in $cc/lib/index.js; reapplying supported fix v$ccv"
    reapply_pkg dsh-client-connection "$pweb" "$fix" || return 20
    reapply_pkg dsh-host-authentication-token "$pweb" "$fix" || return 20
    changed=1
  fi
  # ensure auth-token alignment for supported fix (idempotent)
  reapply_pkg dsh-host-authentication-token "$pweb" "$fix" || return 20
  # Behavior-first probe: if smoke passes, no restart needed.
  local sline
  if sline="$(smoke_check "$web" "$gw")"; then
    log "OK (probe pass): $sline reapplied=$changed"
    printf '%s reapplied=%s\n' "$sline" "$changed"
    return 0
  fi
  log "probe failed: $sline"
  # Probe failed: if files were already "correct" by marker, refresh aligned set once.
  if [ "$changed" = 0 ]; then
    log "marker present but probe failed; refreshing aligned packages from supported fix v$ccv"
    reapply_pkg dsh-client-connection "$pweb" "$fix" || return 20
    reapply_pkg dsh-host-authentication-token "$pweb" "$fix" || return 20
    changed=1
  fi
  printf 'reapplied=%s version=%s fix=%s probe_failed=1\n' "$changed" "$ccv" "$fix"
  return 10
}

cmd_smoke() {
  local web="${WEB_URL:-$(resolve_web_url)}" gw="$GATEWAY_URL" sline
  if sline="$(smoke_check "$web" "$gw")"; then
    log "SMOKE PASS: $sline"
    printf '%s\n' "$sline"
    return 0
  else
    log "SMOKE FAIL: $sline"
    printf '%s\n' "$sline"
    return 30
  fi
}

cmd_reapply_files() {
  local pweb="$PROFILE_WEB" cc at ccv atv fix changed=0
  cc="$(pkg_dir "$pweb" dsh-client-connection)"
  at="$(pkg_dir "$pweb" dsh-host-authentication-token)"
  ccv="$(pkg_version "$cc")"; atv="$(pkg_version "$at")"
  fix="$(find_fix_version "$ccv" || true)"
  if [ -z "$fix" ] || [ "$atv" != "$ccv" ]; then
    log "UNSUPPORTED: dsh-client-connection=$ccv dsh-host-authentication-token=$atv fix=${fix:-none}"
    log "refusing to patch blindly (version mismatch or no durable fix assets)"
    return 20
  fi
  if ! has_noauth_marker "$cc"; then
    reapply_pkg dsh-client-connection "$pweb" "$fix" || return 20
    changed=1
  fi
  reapply_pkg dsh-host-authentication-token "$pweb" "$fix" || return 20
  printf 'reapplied=%s version=%s fix=%s\n' "$changed" "$ccv" "$fix"
  return 0
}

cmd_simulate_copy() { # dest [--break]
  local dest="${1:-}" brk="${2:-}"
  [ -n "$dest" ] || usage
  local src="$PROFILE_WEB/node_modules/@deepseek-ai"
  mkdir -p "$dest/node_modules/@deepseek-ai"
  for pkg in dsh-client-connection dsh-host-authentication-token; do
    [ -e "$src/$pkg" ] || [ -L "$src/$pkg" ] || { log "missing live package $src/$pkg"; exit 1; }
    rm -rf "$dest/node_modules/@deepseek-ai/$pkg"
    mkdir -p "$dest/node_modules/@deepseek-ai/$pkg"
    # Copy essential package content only (avoid huge/cyclic nested node_modules).
    # Dereference symlinks so the sim copy is self-contained.
    if [ -d "$src/$pkg/lib" ] || [ -L "$src/$pkg/lib" ]; then
      cp -R -L "$src/$pkg/lib" "$dest/node_modules/@deepseek-ai/$pkg/lib"
    fi
    for f in package.json LICENSE README.md README.i18n.yaml README.zh.md; do
      [ -e "$src/$pkg/$f" ] && cp -L "$src/$pkg/$f" "$dest/node_modules/@deepseek-ai/$pkg/$f" || true
    done
    if [ -d "$src/$pkg/locale" ] || [ -L "$src/$pkg/locale" ]; then
      cp -R -L "$src/$pkg/locale" "$dest/node_modules/@deepseek-ai/$pkg/locale"
    fi
  done
  if [ "$brk" = "--break" ]; then
    # Revert the noAuth marker so a reapply can be demonstrated on the copy.
    local f="$dest/node_modules/@deepseek-ai/dsh-client-connection/lib/index.js"
    python3 - "$f" <<'PY'
import sys
p=sys.argv[1]
s=open(p,'r',encoding='utf-8').read()
# remove obvious marker token occurrences to simulate overwrite (kept syntactically harmless best-effort)
if 'this.noAuth = noAuth' in s:
    s=s.replace('this.noAuth = noAuth','this.__noauth_removed__ = noAuth')
open(p,'w',encoding='utf-8').write(s)
PY
    log "simulated overwrite: broke noAuth marker in copy at $dest"
  fi
  printf '%s\n' "$dest"
}

# flag parsing
CMD="${1:-}"; shift || true
while [ $# -gt 0 ]; do
  case "$1" in
    --fix-root) FIX_ROOT="$2"; shift 2;;
    --profile-web) PROFILE_WEB="$2"; shift 2;;
    --web-url) WEB_URL="$2"; shift 2;;
    --gateway-url) GATEWAY_URL="$2"; shift 2;;
    -h|--help) usage;;
    *) break;;
  esac
done

case "$CMD" in
  ensure) cmd_ensure ;;
  smoke) cmd_smoke ;;
  reapply-files) cmd_reapply_files ;;
  simulate-copy) cmd_simulate_copy "$@" ;;
  *) usage ;;
esac
