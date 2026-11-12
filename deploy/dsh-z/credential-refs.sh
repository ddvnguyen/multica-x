# Shared helper: project the lane's credential refs ($LANE_HOME/.credentials.yaml
# `refs:` block) into an env-file body. Sourced by deploy.sh and
# sync-runtime-config.sh so both use one code path (M-103, M-126).
#
# Values go straight into the 0600 file and are never passed through log().
# Usage: source this file (with LANE_HOME set), then call emit_lane_credential_refs.

emit_lane_credential_refs() {
  [ -f "$LANE_HOME/.credentials.yaml" ] || return 0
  awk '
    /^refs:[[:space:]]*$/      { in_refs = 1; next }
    in_refs && /^[^[:space:]]/ { in_refs = 0 }
    in_refs && /^  [A-Za-z_][A-Za-z0-9_]*:[[:space:]]/ {
      line = $0
      sub(/^  /, "", line)
      key = line; sub(/:[[:space:]].*$/, "", key)
      val = line; sub(/^[^:]*:[[:space:]]*/, "", val)
      if (val ~ /^".*"$/) { sub(/^"/, "", val); sub(/"$/, "", val) }
      if (length(val) > 0) print key "=" val
    }
  ' "$LANE_HOME/.credentials.yaml"
}