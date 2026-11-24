#!/usr/bin/env bash
# Install a systemd user timer that probes the host side of the production
# stack's published ports every 60 s and restarts a container whose forwarder
# is dead (M-556 option C).
#
# The probe script is copied to ~/.local/bin so the timer does not depend on
# a repository checkout staying alive on disk. Re-run this script to refresh
# the copy and units.
#
# Disable with:  systemctl --user disable --now multica-port-probe.timer
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROBE_SRC="$SCRIPT_DIR/stack-port-probe.sh"
UNITS_DIR="$SCRIPT_DIR/stack-port-probe/units"
BIN_DIR="${HOME}/.local/bin"
UNIT_DIR="${HOME}/.config/systemd/user"
SERVICE=multica-port-probe.service
TIMER=multica-port-probe.timer

[ -f "$PROBE_SRC" ] || { echo "probe script not found: $PROBE_SRC" >&2; exit 1; }
for cmd in install mkdir systemctl; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "required command not found: $cmd" >&2; exit 1; }
done

log() { printf '[install-port-probe %s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

mkdir -p "$BIN_DIR" "$UNIT_DIR" /mnt/workspace/multica-main-port-probe
install -m 755 "$PROBE_SRC" "$BIN_DIR/stack-port-probe.sh"

for u in $SERVICE $TIMER; do
  sed "s|__BIN_DIR__|$BIN_DIR|g" "$UNITS_DIR/$u" > "$UNIT_DIR/.$u.tmp"
  grep -q '__[A-Z_]*__' "$UNIT_DIR/.$u.tmp" && { echo "unsubstituted placeholder in $u" >&2; exit 1; }
  mv "$UNIT_DIR/.$u.tmp" "$UNIT_DIR/$u"
  chmod 600 "$UNIT_DIR/$u"
done

systemctl --user daemon-reload
systemctl --user enable --now "$TIMER"
log "installed: $UNIT_DIR/$TIMER (every 60s)"
log "disable with: systemctl --user disable --now $TIMER"
systemctl --user list-timers "$TIMER" --no-pager || true
log "next: PORT_PROBE_SKIP_LOCK=1 $BIN_DIR/stack-port-probe.sh --check"
